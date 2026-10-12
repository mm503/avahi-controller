# Avahi Controller architecture

A single Deployment writes a managed block in one node's `/etc/avahi/hosts`.
Services are watched cluster-wide through client-go informers. All events collapse
to one queue key; one worker rebuilds desired state. There is no leader election
or multi-writer coordination. The node running avahi-daemon must match the configured
nodeSelector. See README.md and chart values for deployment settings and flags.

A qualifying Service has type LoadBalancer and a nonempty `avahi.homelab/hostname`
annotation. The scan chooses its first valid, nonempty ingress IP and canonicalizes
it. Services without a valid IP cause a retry but do not block other ready entries.
After checking the IP, the scan validates the hostname, warns for missing `.local`,
and resolves conflicts to the oldest ready Service by creation timestamp, then
namespace/name. Names are case-insensitive. Invalid names and conflicts emit a
warning once while the same problem persists. Pending-IP warnings count passes,
not elapsed time.

After cache sync (bounded to one minute), startup logs the existing records and
reconciles. The desired block is sorted by IP and hostname, then hashed. An unchanged
hash skips the write. With reload enabled, startup reloads once even if the file
matches, recovering a possible crash between a previous write and reload.

The controller preserves static bytes outside whole-line BEGIN/END markers. It
stages changes in memory, overwrites the same inode, truncates, and syncs. Ordinary
write failures attempt rollback. A process or host crash can still leave a partial
write: atomic rename is incompatible with the single-file hostPath mount.
Malformed markers stop backoff retries and emit a pod Warning Event; a manual file
repair, Service event, or resync triggers another pass. File metadata is checked
every second. An unlinked mounted inode is rejected and the controller exits;
recreate the pod after a rename-based host edit to bind the replacement file.

Reload is opt-in. Helm `reload.enabled=true` adds the flag, system D-Bus socket and
address together. The default chart and raw deployment do not expose the socket.
A root D-Bus client can start root processes on the host, so enable reload only for
trusted images. `ReloadUnit(unit, "replace")` is bounded to ten seconds; its reply
means the job was accepted, not that avahi finished reloading. Without reload the
controller cannot detect whether avahi-daemon is running.

The default image is `0.6.4`, which lacks health endpoints. The chart leaves
probes disabled unless `healthProbes.enabled=true`; use that setting only with
an image containing the updated controller. The raw manifest omits probes too.
Readiness/liveness HTTP endpoints on port 8080 use the last completed reconciliation
(including passes with pending IPs). They fail after `resync-period + 1m` without
success, except malformed markers fail readiness immediately while liveness stays
successful to allow manual repair without restarting. Three consecutive unexpected failures emit a Warning Event on the pod.
Shutdown stops the queue and waits for in-flight work before optional cleanup;
cleanup reloads use a fresh bounded context.

The pod runs as UID 0 to write the root-owned hosts file, drops all capabilities,
disallows privilege escalation, uses a read-only root filesystem and RuntimeDefault
seccomp. RBAC permits get/list/watch Services and create/patch Events. Images use
a digest-pinned distroless runtime; CI executes the packaged binary and runs race,
vet, module-tidy and vulnerability checks. Version reporting uses `-X main.version`.
