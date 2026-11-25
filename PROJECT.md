## Avahi Controller

### Architecture

Single `Deployment` (replicas: 1) pinned to a designated node via `nodeSelector`. That node runs avahi-daemon and owns all mDNS announcements for all LoadBalancer IPs. The controller watches Services cluster-wide and manages a block in that node's `/etc/avahi/hosts`.

No DaemonSet. No leader election. No multi-writer coordination.

The controller is not in the resolution path. avahi-daemon answers the queries
from the file, so a stopped or restarting pod delays changes but does not stop
existing names from resolving (unless `--cleanup-on-exit` removed the block).
The single point of failure is the designated node and its avahi-daemon; see
[Multiple Nodes](#multiple-nodes-not-implemented).

---

### Annotation Design

```yaml
annotations:
  avahi.homelab/hostname: "myapp.local"
```

- Only acts on `type: LoadBalancer` Services
- Only writes an entry once a `status.loadBalancer.ingress` entry has an IP (the first one with an IP is used)
- Ignores Services without the annotation
- One hostname per Service; no aliases support
- One hostname per IP: avahi publishes only one name per address, so the
  reconciler keeps the first Service to claim an IP
  (see [What avahi Refuses to Publish](#what-avahi-refuses-to-publish))

---

### Node Designation

Label the target node:

```bash
kubectl label node <nodename> avahi-controller=true
```

```yaml
# Deployment nodeSelector
nodeSelector:
  avahi-controller: "true"
```

avahi-daemon must be installed and running on that node. The controller does not manage avahi-daemon itself.

---

### Reconciliation Logic

```
Watch: Services (all namespaces)

All events (Add/Update/Delete) collapse to a single sentinel queue key.
The queue uses exponential backoff (100ms–30s) for retries.

On any event:
  - Rebuild desired state from ALL annotated LoadBalancer Services
  - Hash desired block content
  - Compare to current block in file
  - If changed: write file (avahi-daemon reloads it on its own)

Per-Service filtering during scan:
  - type: LoadBalancer? No → skip
  - Has annotation? No → skip
  - Any .status.loadBalancer.ingress entry with an IP? No → requeue with backoff
  - Annotation is a valid RFC-1123 hostname? No → skip + Warning Event
  - Hostname already claimed with a different IP? → skip + Warning Event
  - Hostname already claimed with the same IP? → skip silently (same record)
  - IP already claimed by another hostname? → skip + Warning Event
  - IP is this node's own address (HOST_IP)? → keep, Warning Event once

On startup:
  - Full reconcile of all Services after cache sync
  - Rewrites file — handles node reboot or manual edits to managed block

Shutdown:
  - if --cleanup-on-exit, removes managed block from hosts file
```

Desired state is always rebuilt from the full Service list, not incrementally patched. Keeps the logic simple and self-healing.

---

### File Management

Own only a marked block, never the full file:

```
# statically managed entries above this line are untouched

### BEGIN k8s-avahi-controller ###
# Managed by avahi-controller. Do not edit between these markers.
192.168.1.100 myapp.local
192.168.1.101 otherapp.local
### END k8s-avahi-controller ###
```

Write sequence:
1. Read current file
2. Replace content between markers (or append block if markers absent); stop here if nothing changed, without opening the file for writing
3. Stage the updated content in memory, then overwrite and truncate the existing file inode (created with 0644 permissions if absent). Replacing the inode would break the single-file hostPath mount.

Malformed or ambiguous block markers cause reconciliation to fail without changing the file. Ordinary write errors trigger a best-effort restore of the prior content. A sudden process or host failure during an in-place write can still leave partial content; atomic replacement is incompatible with the current single-file mount.

Entries inside the block are sorted by IP, then hostname (deterministic even when Services share an IP). Skip the write entirely if the content hash matches the current block — avoids unnecessary avahi reloads on pod restarts when state is already correct.

---

### How avahi-daemon Picks Up Changes

The controller never signals avahi-daemon; it relies on avahi's own file
watching. From the avahi source (`avahi-daemon/main.c`, `static-hosts.c`):

- At startup avahi-daemon adds an inotify watch on its config directory
  (`AVAHI_CONFIG_DIR`, i.e. `/etc/avahi`) for `IN_CLOSE_WRITE`, deletes and
  moves (`add_inotify_watches()`). It is compiled in when `HAVE_INOTIFY` is
  set, which `configure` enables on Linux.
- Any event calls `inotify_callback()`, which logs `Files changed, reloading.`
  and runs `reload_config()`.
- `reload_config()` calls `static_hosts_load()`, which re-reads
  `/etc/avahi/hosts`, adds new names and frees names whose line vanished.
- SIGHUP (`systemctl reload avahi-daemon` → `avahi-daemon -r`) calls the same
  `reload_config()`. That is why the former `--reload` flag, which went through
  systemd on the host D-Bus, was redundant and was removed along with the D-Bus
  socket mount.

Consequences for the controller:

- `IN_CLOSE_WRITE` fires when a file *opened for writing* is closed, even if
  nothing was written. The reconciler compares hashes before calling
  `WriteBlock`, and `WriteBlock` itself reads the file and returns early when
  the content would not change (for example `--cleanup-on-exit` with no
  managed block). Unchanged passes therefore never open the file for writing
  and never make avahi reload. Tests assert this by making the file read-only.
- The parser reads one IP and one hostname per line, ignores `#` comments and
  blank lines, and on any invalid line (aliases, bad address, missing
  hostname) logs an error and stops (`goto fail`). Entries after that line are
  not loaded and vanished names are not removed. The managed block is appended
  at the end of the file, so a bad static line above it hides every managed
  entry.
- Lines are read with a 256-byte buffer (`fgets`). A line longer than 255
  bytes (excluding the newline) is split: the first part is published with a
  truncated hostname and the remainder fails to parse, stopping the load. The
  reconciler skips such entries (`hostsfile.MaxLineLength`) with a
  `HostsLineTooLong` Warning Event; a 253-byte hostname is legal DNS but can
  exceed the line limit once the IP is added.
- If avahi-daemon is stopped, the controller keeps writing; avahi loads the
  file when it starts.

---

### What avahi Refuses to Publish

For every hosts line avahi calls `avahi_server_add_address()` with no flags
(`static-hosts.c`). That adds the A/AAAA record and also a reverse PTR record
(address → name). The A record is added with `AVAHI_PUBLISH_ALLOW_MULTIPLE`;
the PTR is `AVAHI_PUBLISH_UNIQUE` only (`avahi-core/entry.c`), so each address
can have one name. The hosts file has no way to switch the reverse record off
(`AVAHI_PUBLISH_NO_REVERSE` is reachable only through the API, as in
`avahi-publish -a -R`). Checked against avahi 0.8:

- **Two names for one address.** One is published and the other fails with
  `Static host name X: avahi_server_add_address failure: Local name collision`.
  A name that is already published keeps the address. When both are loaded in
  the same pass (avahi start), entries are added in reverse file order, so the
  last line for the address wins; the block is sorted by IP then hostname.
  This is what happens with MetalLB IP sharing. The reconciler therefore
  never writes two hostnames for one address: the Service with the lowest
  namespace/name keeps the IP and the others are skipped with a
  `SharedIPConflict` Event. Addresses are compared in canonical form, so two
  spellings of one IPv6 address count as the same IP.
- **A node's own address.** avahi already publishes a PTR for each local
  address under the node's hostname. An entry for that address fails with
  `Local name collision` on that node; written on a different node it is
  probed, the owning node answers, and it ends as `Host name conflict for "X",
  not established`. k3s ServiceLB assigns node addresses, so nothing it
  exposes can be published.

The controller has no channel back from avahi, so it cannot see either
failure; it predicts them instead. Shared IPs are resolved before the file is
written. For node addresses it knows only its own node's address (`HOST_IP`,
from `status.hostIP`): when a LoadBalancer IP matches, it logs a warning and
emits a `NodeAddress` Event once, and still writes the line in case avahi is
configured with `publish-addresses=no`. Addresses of other nodes are not
checked; that would need RBAC on Nodes, and such an entry does work when the
owning node runs no avahi-daemon.

Publishing several names per address is not possible through the hosts file.

---

### Multiple Nodes (not implemented)

One pod can write one node's file, so one node answers for everything. If that
node is down, names expire from client caches within the record TTL (120 s),
even though MetalLB has moved the IP elsewhere.

A single replica that floats between labelled nodes is not a fix: the file on
the node it left keeps its block, and that node publishes the stale block
whenever its avahi-daemon is running.

Running one pod per avahi node is sound, because every node would publish the
same name → load balancer IP records and none of them refers to the node
itself. Measured with three avahi 0.8 daemons on one segment (the relevant
code is `handle_conflict()` and `incoming_probe()` in `avahi-core/server.c`):

| Situation | Result |
|---|---|
| Two nodes, same name, same address | Both log `successfully established`. An identical record is treated as "ours", not a conflict |
| One of them stops | It sends goodbye records; the other logs `Received goodbye record for one of our records … Refreshing` and keeps answering |
| IP changes; node B reloads while node A still holds the old address | B's probe is answered by A. B logs `Received conflicting record … Withdrawing` and `Host name conflict for "X", not established` |
| A then reloads with the new address | A establishes it. B does **not** retry by itself |
| B's hosts file is opened for writing and closed, unchanged | avahi reloads, finds B's entry group empty and re-adds it: `successfully established` |

The node that updates last always ends up publishing, so the name stays
resolvable; the nodes that updated earlier are silent for that name until
their next reload. With both nodes written at the same moment, 5 of 24 updates
conflicted; with the second node 300 ms or 1 s behind, the first node lost
every time. New names and removed names never conflict; only a changed address
for an existing name does.

What it would take:

1. **Chart:** a DaemonSet (or an option for one) restricted to nodes with
   avahi-daemon. Each pod writes only its own node's file, so there is still
   no shared state and no leader election.
2. **Controller:** after a write, open and close the file once more a few
   seconds later (and on each resync) so a node that lost the probe publishes
   again. A reload leaves established entries alone. This deliberately
   reverses, for that one case, the rule that an unchanged pass never opens
   the file for writing.
3. **Stale nodes:** a node whose pod is gone but whose avahi-daemon is running
   keeps publishing its last block. For a changed address it holds the old
   record and wins every probe from the other nodes, so the new address is not
   published anywhere until that file is fixed. `--cleanup-on-exit=true`
   should be the default in this mode; it covers a graceful stop but not a
   crash.

---

### Pod ↔ Host File

The pod mounts the single file `/etc/avahi/hosts` via `hostPath` (`type:
FileOrCreate`), not the `/etc/avahi` directory. Mounting the directory would
also expose `avahi-daemon.conf` and `services/` to the pod.

A single-file hostPath is a bind mount of the file's inode at pod start, which
drives two rules:

- The controller must rewrite the file in place (same inode). Inside the pod
  the path is a mount point, so the usual write-temp-then-rename pattern cannot
  replace it, and atomic replacement is not available.
- If something on the host replaces the file (rename-based editors, config
  management tools), the pod stays bound to the old, unlinked inode. Its writes
  succeed but nothing reads them. Restarting the pod binds the new file.

Because the in-place write goes to the inode avahi-daemon's directory watch
covers, closing it generates `IN_CLOSE_WRITE` on the host and avahi reloads.

The container runs as `runAsUser: 0` with all capabilities dropped. That is
enough to write the root-owned `0644` hosts file as its owner, and grants no
other privilege (no `DAC_OVERRIDE`).

---

### CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | `` | Path to kubeconfig (empty = in-cluster) |
| `--cleanup-on-exit` | `false` | Remove managed block on shutdown |
| `--resync-period` | `10m` | Informer resync period |
| `--verbose` | `false` | Log reconcile details (qualifying services, scan summary) |
| `--debug` | `false` | Log everything including client-go internals |

Environment (both optional, set from the downward API): `NODE_NAME` is the
source host on Events; `HOST_IP` is the node address, a comma-separated list
is accepted, and it enables the `NodeAddress` warning.

---

### Deployment Spec (Key Parts)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: avahi-controller
  namespace: kube-system
spec:
  replicas: 1
  strategy:
    type: Recreate
  template:
    spec:
      serviceAccountName: avahi-controller
      nodeSelector:
        avahi-controller: "true"
      tolerations:
        - operator: Exists        # in case the designated node has taints
      volumes:
        - name: avahi-hosts
          hostPath:
            path: /etc/avahi/hosts
            type: FileOrCreate
      containers:
        - name: controller
          image: ghcr.io/mm503/avahi-controller:<version>
          securityContext:
            seccompProfile:
              type: RuntimeDefault
            runAsUser: 0                # owner of the root-owned hosts file
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
          volumeMounts:
            - name: avahi-hosts
              mountPath: /etc/avahi/hosts
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
            - name: HOST_IP             # optional: enables the NodeAddress warning
              valueFrom:
                fieldRef:
                  fieldPath: status.hostIP
```

---

### RBAC

```yaml
rules:
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["list", "watch"]
  - apiGroups: [""]
    resources: ["events"]
    verbs: ["create", "patch"]
```

`ClusterRole` + `ClusterRoleBinding` — Services are watched across all namespaces.

---

### Edge Cases

| Scenario | Handling |
|---|---|
| MetalLB reassigns IP | Update event → reconcile rewrites entry |
| Service deleted | Delete event → entry removed |
| Node reboots, file wiped | Pod reschedules to same node (pinned), startup reconcile rewrites |
| Two Services claim same hostname | Log error, emit k8s Warning Event, skip loser — lowest namespace/name wins deterministically |
| Annotation added to existing Service | Update event → treated as new entry |
| Annotation is not a valid hostname | Log error, emit k8s Warning Event, skip — value is never written to the file |
| `IP hostname` line longer than 255 bytes | Log error, emit `HostsLineTooLong` Warning Event, skip — avahi cannot read longer lines |
| Services with different hostnames share one LoadBalancer IP | Log error, emit `SharedIPConflict` Warning Event, skip loser — lowest namespace/name wins |
| Services with the same hostname share one LoadBalancer IP | One line written, no Event |
| LoadBalancer IP is this node's address (k3s ServiceLB) | Line written; avahi normally rejects it. Log warning and emit `NodeAddress` Warning Event once |
| LoadBalancer IP is another node's address | Line written; rejected if that node runs avahi-daemon. Not detected by the controller |
| Designated node or its avahi-daemon is down | Names stop resolving as client caches expire (120 s TTL); nothing takes over |
| Annotation removed | Update event → entry removed, same as delete |
| Pod restarts, file already correct | Hash match → file not opened for writing, no avahi reload |
| avahi-daemon stopped or restarted | Controller keeps writing; avahi loads the file at startup |
| Invalid static line above the block | avahi stops parsing there; managed entries are not published until it is fixed |
| Hosts file replaced on the host (rename) | Pod keeps writing the old inode; restart the pod |

---

### Implementation Stack

- `k8s.io/client-go` — SharedInformerFactory, rate-limiting work queue, no controller-runtime needed
- Raw informers are sufficient — under 1,000 lines of Go, single binary, minimal image
