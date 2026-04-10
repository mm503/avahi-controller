// Package reconciler implements the core reconciliation logic for the avahi controller.
// It builds desired state from all annotated LoadBalancer Services and conditionally
// writes the hosts file and reloads avahi-daemon.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"github.com/mm503/avahi-controller/internal/avahi"
	"github.com/mm503/avahi-controller/internal/events"
	"github.com/mm503/avahi-controller/internal/hostsfile"
)

const annotationHostname = "avahi.homelab/hostname"

// ErrMissingIP is returned when a qualifying Service has no LoadBalancer IP yet.
// The caller should requeue with backoff.
var ErrMissingIP = fmt.Errorf("service has no LoadBalancer IP yet")

// Reconciler performs full desired-state reconciliation on every call.
type Reconciler struct {
	Lister   corev1listers.ServiceLister
	HostsMgr *hostsfile.Manager
	Reloader avahi.Reloader // nil means reload is disabled
	Recorder *events.Recorder
	Client   kubernetes.Interface
}

// Reconcile scans all Services, builds desired state, and writes the hosts file if changed.
// Returns ErrMissingIP (wrapped) if any qualifying Service is still pending an IP —
// the caller should requeue.
func (r *Reconciler) Reconcile(_ context.Context) error {
	desired, needsRequeue, err := r.buildDesiredEntries()
	if err != nil {
		return fmt.Errorf("build desired entries: %w", err)
	}

	wantHash := r.HostsMgr.HashBlock(desired)
	gotHash, err := r.HostsMgr.HashCurrentBlock()
	if err != nil {
		return fmt.Errorf("hash current block: %w", err)
	}

	if wantHash == gotHash {
		slog.Debug("hosts file up to date, skipping write", "entries", len(desired))
	} else {
		slog.Info("writing hosts file", "entries", len(desired))
		if err := r.HostsMgr.WriteBlock(desired); err != nil {
			return fmt.Errorf("write hosts block: %w", err)
		}
		if r.Reloader != nil {
			if err := r.Reloader.Reload(); err != nil {
				return fmt.Errorf("reload avahi: %w", err)
			}
			slog.Info("avahi reloaded")
		}
	}

	if needsRequeue {
		return fmt.Errorf("%w: one or more services pending IP assignment", ErrMissingIP)
	}
	return nil
}

// buildDesiredEntries scans all Services from the in-memory lister and returns:
//   - the set of HostEntry values for all qualifying, ready Services
//   - needsRequeue=true if any qualifying Service is still awaiting an IP
func (r *Reconciler) buildDesiredEntries() ([]hostsfile.HostEntry, bool, error) {
	svcs, err := r.Lister.List(labels.Everything())
	if err != nil {
		return nil, false, fmt.Errorf("list services: %w", err)
	}

	// hostname → "namespace/name" of the first Service to claim it (conflict detection).
	claimed := make(map[string]string)
	var entries []hostsfile.HostEntry
	needsRequeue := false
	skipped := 0

	for _, svc := range svcs {
		if !r.qualifies(svc) {
			skipped++
			continue
		}

		ip := loadBalancerIP(svc)
		if ip == "" {
			slog.Debug("waiting for LoadBalancer IP", "service", svc.Namespace+"/"+svc.Name)
			needsRequeue = true
			continue
		}

		hostname := strings.TrimSpace(svc.Annotations[annotationHostname])
		key := svc.Namespace + "/" + svc.Name

		if owner, conflict := claimed[hostname]; conflict {
			slog.Error("hostname conflict, skipping service", "hostname", hostname, "owner", owner, "skipped", key)
			if r.Recorder != nil {
				r.Recorder.Warnf(svc, "HostnameConflict",
					"hostname %q is already claimed by %s", hostname, owner)
			}
			continue
		}
		claimed[hostname] = key

		slog.Debug("include service", "service", key, "ip", ip, "hostname", hostname)
		entries = append(entries, hostsfile.HostEntry{
			IP:       ip,
			Hostname: hostname,
		})
	}

	slog.Debug("scan complete", "total", len(svcs), "qualifying", len(entries), "skipped", skipped)
	return entries, needsRequeue, nil
}

// qualifies returns true if the Service should be managed by this controller.
func (r *Reconciler) qualifies(svc *corev1.Service) bool {
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return false
	}
	hostname, ok := svc.Annotations[annotationHostname]
	return ok && strings.TrimSpace(hostname) != ""
}

// loadBalancerIP returns the first allocated LoadBalancer IP, or "" if not yet assigned.
func loadBalancerIP(svc *corev1.Service) string {
	if len(svc.Status.LoadBalancer.Ingress) == 0 {
		return ""
	}
	return svc.Status.LoadBalancer.Ingress[0].IP
}
