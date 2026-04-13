## Avahi Controller

### Architecture

Single `Deployment` (replicas: 1) pinned to a designated node via `nodeSelector`. That node runs avahi-daemon and owns all mDNS announcements for all LoadBalancer IPs. The controller watches Services cluster-wide and manages a block in that node's `/etc/avahi/hosts`.

No DaemonSet. No leader election. No multi-writer coordination.

---

### Annotation Design

```yaml
annotations:
  avahi.homelab/hostname: "myapp.local"
```

- Only acts on `type: LoadBalancer` Services
- Only writes entry when `status.loadBalancer.ingress[0].ip` is populated
- Ignores Services without the annotation
- One hostname per Service; no aliases support

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
  - If changed: write file, optionally reload avahi (--reload flag)

Per-Service filtering during scan:
  - type: LoadBalancer? No → skip
  - Has annotation? No → skip
  - Has .status.loadBalancer.ingress[0].ip? No → requeue with backoff

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
2. Replace content between markers (or append block if markers absent)
3. Write via `os.WriteFile` with 0644 permissions (readable by avahi user)

Entries inside the block are sorted by IP. Skip write + reload entirely if content hash matches current block — avoids unnecessary reloads on pod restarts when state is already correct.

---

### Avahi Reload

Reload is **opt-in** via `--reload` flag. By default avahi-daemon watches `/etc/avahi/hosts` itself and picks up changes without any signal.

When `--reload` is set, the controller signals avahi-daemon via **systemd on the system D-Bus**:

```
org.freedesktop.systemd1.Manager.ReloadUnit(<service>, "replace")
```

This is equivalent to `systemctl reload <service>` and sends SIGHUP to avahi-daemon, causing it to re-read its hosts file.

The systemd service name defaults to `avahi-daemon.service` and can be overridden with `--avahi-service`.

The system D-Bus socket must be mounted into the pod when `--reload` is used.

---

### CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | `` | Path to kubeconfig (empty = in-cluster) |
| `--cleanup-on-exit` | `false` | Remove managed block on shutdown |
| `--avahi-service` | `avahi-daemon.service` | systemd unit name for avahi-daemon |
| `--resync-period` | `10m` | Informer resync period |
| `--verbose` | `false` | Log reconcile details (qualifying services, scan summary) |
| `--debug` | `false` | Log everything including client-go internals |
| `--reload` | `false` | Signal avahi-daemon via systemd after writing hosts file |

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
        - name: dbus-socket       # only needed when --reload is set
          hostPath:
            path: /run/dbus/system_bus_socket
            type: Socket
      containers:
        - name: controller
          image: your-registry/avahi-controller:latest
          securityContext:
            capabilities:
              add: ["DAC_OVERRIDE"]   # file write, no full privileged needed
          volumeMounts:
            - name: avahi-hosts
              mountPath: /etc/avahi/hosts
            - name: dbus-socket
              mountPath: /run/dbus/system_bus_socket
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef:
                  fieldPath: spec.nodeName
```

---

### RBAC

```yaml
rules:
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["events.k8s.io"]
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
| Two Services claim same hostname | Log error, emit k8s Warning Event, skip second — first writer wins |
| Annotation added to existing Service | Update event → treated as new entry |
| Annotation removed | Update event → entry removed, same as delete |
| avahi-daemon not running | Log error, retry with exponential backoff |
| Pod restarts, file already correct | Hash match → no write, no reload |

---

### Implementation Stack

- `k8s.io/client-go` — SharedInformerFactory, rate-limiting work queue, no controller-runtime needed
- `github.com/godbus/dbus/v5` — systemd D-Bus for optional avahi reload
- Raw informers are sufficient — ~400 lines of Go, single binary, minimal image
