// Package reconciler implements the core reconciliation logic for the avahi controller.
// It builds desired state from all annotated LoadBalancer Services and writes the
// hosts file only when the managed block changes. avahi-daemon watches its config
// directory with inotify and reloads the file itself, so every write is published
// without any signal from the controller.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"

	"github.com/mm503/avahi-controller/internal/hostsfile"
)

const annotationHostname = "avahi.homelab/hostname"

// pendingIPEventThreshold is the number of consecutive reconcile passes a
// qualifying Service may spend without a LoadBalancer IP before a Warning
// event is emitted. This counts passes, not elapsed time: Service events,
// file changes and retry backoff all affect how quickly it is reached. A
// persistent lack of an IP may indicate a misconfigured or exhausted pool.
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
// IP-shaped values match the RFC-1123 pattern but are almost certainly a
// mixed-up annotation (they would produce an "IP IP" hosts line), so they
// are rejected too.
func validHostname(s string) bool {
	return len(s) <= 253 && hostnamePattern.MatchString(s) && net.ParseIP(s) == nil
}

// WarningRecorder emits Warning Events for Services rejected or still pending.
type WarningRecorder interface {
	Warnf(*corev1.Service, string, string, ...any)
}

// Reconciler performs full desired-state reconciliation on every call.
type Reconciler struct {
	Lister   corev1listers.ServiceLister
	HostsMgr *hostsfile.Manager
	Recorder WarningRecorder

	// NodeIPs holds the addresses of the node whose avahi-daemon publishes
	// the hosts file (HOST_IP in the pod). A LoadBalancer IP equal to one of
	// them is written but triggers a warning, because avahi-daemon already
	// publishes that address under the node's hostname and rejects a second
	// name for it. Empty disables the check.
	NodeIPs []string

	// pendingIPCounts tracks how many consecutive reconcile passes each
	// qualifying Service (namespace/name) has spent without a LoadBalancer
	// IP, so a stuck assignment can be surfaced as a Warning event instead
	// of requeuing silently forever. Only touched by the single worker
	// goroutine.
	pendingIPCounts map[string]int

	// warnedOnce remembers the advisory warnings (hostname outside .local,
	// LoadBalancer IP that is a node address) already raised for a Service,
	// so each fires once instead of on every reconcile pass. The entry is
	// still published in these cases. Only touched by the single worker
	// goroutine.
	warnedOnce map[string]bool

	// startupLogged is set after the first reconcile pass has logged the
	// entries found in the hosts file, so the inventory is printed exactly
	// once per process start. Only touched by the single worker goroutine.
	startupLogged bool
}

// Reconcile scans all Services, builds desired state, and writes the hosts file if changed.
// Returns ErrMissingIP (wrapped) if any qualifying Service is still pending an IP —
// the caller should requeue.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !r.startupLogged {
		r.startupLogged = true
		r.logStartupInventory()
	}

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
	}

	if needsRequeue {
		return fmt.Errorf("%w: one or more services pending IP assignment", ErrMissingIP)
	}
	return nil
}

// logStartupInventory logs every hostname/IP entry currently present in the
// hosts file's managed block. It runs once, on the first reconcile after
// startup, so a pod restart always shows what was published before any
// incremental add/remove logging takes over.
func (r *Reconciler) logStartupInventory() {
	entries, err := r.HostsMgr.ReadBlock()
	if err != nil {
		slog.Warn("could not read hosts file for startup inventory", "error", err)
		return
	}
	slog.Info("startup: existing DNS records in hosts file", "entries", len(entries))
	for _, e := range entries {
		slog.Info("existing DNS record", "hostname", e.Hostname, "ip", e.IP)
	}
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

	// lowercased hostname → the first Service to claim it. DNS/mDNS names are
	// case-insensitive, so Foo.local and foo.local conflict.
	claimed := make(map[string]claim)
	// normalized IP → the first Service to claim it. avahi-daemon publishes
	// a reverse record for every hosts line and allows one per address, so
	// it rejects a second hostname for an IP ("Local name collision").
	claimedIP := make(map[string]claim)
	var entries []hostsfile.HostEntry
	needsRequeue := false
	skipped := 0
	pendingIP := make(map[string]int)
	warned := make(map[string]bool)

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

		entry := hostsfile.HostEntry{IP: ip, Hostname: hostname}
		if len(entry.Line()) > hostsfile.MaxLineLength {
			slog.Error("hosts line too long for avahi-daemon, skipping service",
				"hostname", hostname, "ip", ip, "service", key)
			if r.Recorder != nil {
				r.Recorder.Warnf(svc, "HostsLineTooLong",
					"entry %q is longer than the %d bytes avahi-daemon can read", entry.Line(), hostsfile.MaxLineLength)
			}
			continue
		}

		hostnameKey := strings.ToLower(hostname)
		ipKey := normalizeIP(ip)

		// mDNS resolvers only look up names under .local; anything else is
		// published by avahi but effectively unreachable. Warn once.
		if !strings.HasSuffix(hostnameKey, ".local") {
			warnKey := "nonlocal " + key + " " + hostnameKey
			warned[warnKey] = true
			if !r.warnedOnce[warnKey] {
				slog.Warn("hostname does not end in .local, mDNS clients will not resolve it",
					"hostname", hostname, "service", key)
				if r.Recorder != nil {
					r.Recorder.Warnf(svc, "NonLocalHostname",
						"hostname %q does not end in .local and will not be resolvable via mDNS", hostname)
				}
			}
		}

		if owner, conflict := claimed[hostnameKey]; conflict {
			if owner.ipKey == ipKey {
				// Another Service already publishes this exact record (for
				// example TCP and UDP Services sharing one address).
				slog.Debug("record already published by another service", "hostname", hostname, "ip", ip, "owner", owner.service, "service", key)
				continue
			}
			slog.Error("hostname conflict, skipping service", "hostname", hostname, "owner", owner.service, "skipped", key)
			if r.Recorder != nil {
				r.Recorder.Warnf(svc, "HostnameConflict",
					"hostname %q is already claimed by %s", hostname, owner.service)
			}
			continue
		}

		if owner, shared := claimedIP[ipKey]; shared {
			slog.Error("IP already has a hostname, skipping service: avahi-daemon publishes one hostname per address",
				"ip", ip, "hostname", hostname, "owner", owner.service, "owner_hostname", owner.hostname, "skipped", key)
			if r.Recorder != nil {
				r.Recorder.Warnf(svc, "SharedIPConflict",
					"IP %s is already published as %q by %s; avahi-daemon publishes one hostname per address, so %q is not published",
					ip, owner.hostname, owner.service, hostname)
			}
			continue
		}

		// avahi-daemon publishes the node's own addresses under the node's
		// hostname and rejects another name for them. The controller cannot
		// see that rejection, so say it here. Warn once; the entry is still
		// written in case avahi is configured not to publish its addresses.
		if r.isNodeIP(ipKey) {
			warnKey := "nodeip " + key + " " + ipKey
			warned[warnKey] = true
			if !r.warnedOnce[warnKey] {
				slog.Warn("LoadBalancer IP is the node's own address, avahi-daemon normally refuses to publish another name for it",
					"ip", ip, "hostname", hostname, "service", key)
				if r.Recorder != nil {
					r.Recorder.Warnf(svc, "NodeAddress",
						"IP %s is an address of the node running avahi-daemon, which normally refuses to publish another name for it; %q will probably not resolve. Use a load balancer that assigns dedicated addresses",
						ip, hostname)
				}
			}
		}

		owner := claim{service: key, hostname: hostname, ipKey: ipKey}
		claimed[hostnameKey] = owner
		claimedIP[ipKey] = owner

		slog.Debug("include service", "service", key, "ip", ip, "hostname", hostname)
		entries = append(entries, entry)
	}

	// Replace rather than update in place so services that got their IP (or
	// stopped qualifying) drop out and start fresh if they regress later.
	r.pendingIPCounts = pendingIP
	r.warnedOnce = warned

	slog.Debug("scan complete", "total", len(svcs), "qualifying", len(entries), "skipped", skipped)
	return entries, needsRequeue, nil
}

// claim records which Service owns a hostname or an IP in the current pass.
type claim struct {
	service  string // namespace/name
	hostname string
	ipKey    string
}

// normalizeIP returns the canonical text form of an IP address so that
// different spellings of one address compare equal. Text that does not parse
// is returned unchanged.
func normalizeIP(s string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return s
	}
	return addr.Unmap().String()
}

// isNodeIP reports whether the normalized IP is one of the node's own addresses.
func (r *Reconciler) isNodeIP(ipKey string) bool {
	for _, nodeIP := range r.NodeIPs {
		if normalizeIP(nodeIP) == ipKey {
			return true
		}
	}
	return false
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
