// Package reconciler implements the core reconciliation logic for the avahi controller.
// It builds desired state from all annotated LoadBalancer Services and conditionally
// writes the hosts file and reloads avahi-daemon.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"github.com/mm503/avahi-controller/internal/avahi"
	"github.com/mm503/avahi-controller/internal/events"
	"github.com/mm503/avahi-controller/internal/hostsfile"
)

const annotationHostname = "avahi.homelab/hostname"

// pendingIPEventThreshold is the number of consecutive reconcile passes a
// qualifying Service may spend without a LoadBalancer IP before a Warning
// event is emitted. With the controller's exponential backoff (capped at
// 30s) this corresponds to roughly six minutes — well past normal MetalLB
// assignment time, so hitting it usually means a misconfigured or exhausted
// address pool.
const pendingIPEventThreshold = 20

// ErrMissingIP is returned when a qualifying Service has no LoadBalancer IP yet.
// The caller should requeue with backoff.
var ErrMissingIP = fmt.Errorf("service has no LoadBalancer IP yet")

// hostnamePattern matches RFC-1123 hostnames (dot-separated alphanumeric
// labels, hyphens allowed inside a label). The annotation value is written
// verbatim into the hosts file, so anything else — embedded whitespace,
// newlines, marker text — must be rejected to protect file integrity.
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// validHostname reports whether s is safe and valid for a hosts file entry.
func validHostname(s string) bool {
	return len(s) <= 253 && hostnamePattern.MatchString(s)
}

// Reconciler performs full desired-state reconciliation on every call.
type Reconciler struct {
	Lister   corev1listers.ServiceLister
	HostsMgr *hostsfile.Manager
	Reloader avahi.Reloader // nil means reload is disabled
	Recorder *events.Recorder

	// reloadPending is set after a successful write whose reload failed, so
	// the retry attempts the reload even though the file content already
	// matches. Only touched by the single worker goroutine.
	reloadPending bool

	// pendingIPCounts tracks how many consecutive reconcile passes each
	// qualifying Service (namespace/name) has spent without a LoadBalancer
	// IP, so a stuck assignment can be surfaced as a Warning event instead
	// of requeuing silently forever. Only touched by the single worker
	// goroutine.
	pendingIPCounts map[string]int
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
		if old, err := r.HostsMgr.ReadBlock(); err == nil {
			logDiff(old, desired)
		}
		slog.Info("writing hosts file", "entries", len(desired))
		if err := r.HostsMgr.WriteBlock(desired); err != nil {
			return fmt.Errorf("write hosts block: %w", err)
		}
		r.reloadPending = r.Reloader != nil
	}

	if r.reloadPending {
		if err := r.Reloader.Reload(); err != nil {
			return fmt.Errorf("reload avahi: %w", err)
		}
		r.reloadPending = false
		slog.Info("avahi reloaded")
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

	// The lister returns services in nondeterministic (map) order. Sort by
	// namespace/name so hostname conflicts resolve to the same winner on
	// every pass instead of flapping between reconciles.
	sort.Slice(svcs, func(i, j int) bool {
		if svcs[i].Namespace != svcs[j].Namespace {
			return svcs[i].Namespace < svcs[j].Namespace
		}
		return svcs[i].Name < svcs[j].Name
	})

	// lowercased hostname → "namespace/name" of the first Service to claim it.
	// DNS/mDNS names are case-insensitive, so Foo.local and foo.local conflict.
	claimed := make(map[string]string)
	var entries []hostsfile.HostEntry
	needsRequeue := false
	skipped := 0
	pendingIP := make(map[string]int)

	for _, svc := range svcs {
		if !r.qualifies(svc) {
			skipped++
			continue
		}

		key := svc.Namespace + "/" + svc.Name

		ip := loadBalancerIP(svc)
		if ip == "" {
			count := r.pendingIPCounts[key] + 1
			pendingIP[key] = count
			if count == pendingIPEventThreshold {
				slog.Warn("service still has no LoadBalancer IP", "service", key, "attempts", count)
				if r.Recorder != nil {
					r.Recorder.Warnf(svc, "PendingLoadBalancerIP",
						"service still has no LoadBalancer IP after %d reconcile attempts", count)
				}
			} else {
				slog.Debug("waiting for LoadBalancer IP", "service", key)
			}
			needsRequeue = true
			continue
		}

		hostname := strings.TrimSpace(svc.Annotations[annotationHostname])

		if !validHostname(hostname) {
			slog.Error("invalid hostname annotation, skipping service", "hostname", hostname, "service", key)
			if r.Recorder != nil {
				r.Recorder.Warnf(svc, "InvalidHostname",
					"annotation %s value %q is not a valid hostname", annotationHostname, hostname)
			}
			continue
		}

		hostnameKey := strings.ToLower(hostname)
		if owner, conflict := claimed[hostnameKey]; conflict {
			slog.Error("hostname conflict, skipping service", "hostname", hostname, "owner", owner, "skipped", key)
			if r.Recorder != nil {
				r.Recorder.Warnf(svc, "HostnameConflict",
					"hostname %q is already claimed by %s", hostname, owner)
			}
			continue
		}
		claimed[hostnameKey] = key

		slog.Debug("include service", "service", key, "ip", ip, "hostname", hostname)
		entries = append(entries, hostsfile.HostEntry{
			IP:       ip,
			Hostname: hostname,
		})
	}

	// Replace rather than update in place so services that got their IP (or
	// stopped qualifying) drop out and start fresh if they regress later.
	r.pendingIPCounts = pendingIP

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

// logDiff logs Info-level messages for DNS records that were added, removed, or updated.
func logDiff(old, desired []hostsfile.HostEntry) {
	oldMap := make(map[string]string, len(old))
	for _, e := range old {
		oldMap[e.Hostname] = e.IP
	}
	newMap := make(map[string]string, len(desired))
	for _, e := range desired {
		newMap[e.Hostname] = e.IP
	}

	for _, e := range desired {
		if oldIP, exists := oldMap[e.Hostname]; !exists {
			slog.Info("adding DNS record", "hostname", e.Hostname, "ip", e.IP)
		} else if oldIP != e.IP {
			slog.Info("updating DNS record", "hostname", e.Hostname, "ip", e.IP, "old_ip", oldIP)
		}
	}
	for _, e := range old {
		if _, exists := newMap[e.Hostname]; !exists {
			slog.Info("removing DNS record", "hostname", e.Hostname, "ip", e.IP)
		}
	}
}

// loadBalancerIP returns the first allocated LoadBalancer IP, or "" if not yet
// assigned. Ingress entries without an IP (e.g. hostname-only entries from
// cloud LBs) are skipped rather than treated as "still pending".
func loadBalancerIP(svc *corev1.Service) string {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if ing.IP != "" {
			return ing.IP
		}
	}
	return ""
}
