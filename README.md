# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and updates a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change automatically (it watches the file by default).

This can be particularly useful in combination with MetalLB, which assigns LAN IPs to your Kubernetes LoadBalancers.

Please note, this is not a production tool. mDNS is not a production-level solution. This is for a home lab and simple Kubernetes clusters.

## Annotation

Add to any `type: LoadBalancer` Service:

```yaml
annotations:
  avahi.homelab/hostname: "myapp.local"
```

The controller ignores Services without this annotation, and requeues with backoff if a LoadBalancer IP hasn't been assigned yet.

## Hosts file block

The controller owns only a marked block and never touches the rest of the file:

```
# your static entries above are untouched

### BEGIN k8s-avahi-controller ###
# Managed by avahi-controller. Do not edit between these markers.
192.168.1.100 myapp.local
192.168.1.101 otherapp.local
### END k8s-avahi-controller ###
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig; empty uses in-cluster config |
| `--cleanup-on-exit` | `false` | Remove managed block from Avahi hosts on controller shutdown |
| `--reload` | `false` | Signal avahi-daemon via systemd D-Bus after each write (modern avahi watches hosts files for changes)|
| `--avahi-service` | `avahi-daemon.service` | systemd unit name (override for distros using `avahi.service`) |
| `--resync-period` | `10m` | Positive informer resync interval; zero and negative values are rejected |
| `--verbose` | `false` | Verbose logging: per-service reconcile details and scan summary |
| `--debug` | `false` | Debug logging: same as verbose plus client-go internals (very noisy) |
| `--version` | `false` | Print build version and exit |

## Avahi reload

By default (`--reload=false`) avahi-daemon notices file changes on its own — no signalling needed. Enable `--reload` only if your setup requires an explicit reload. When enabled, the controller calls `systemctl reload <avahi-service>` via the system D-Bus, which sends SIGHUP to avahi-daemon.

The default install does not mount the host D-Bus socket. Helm adds the socket,
address environment variable, and flag together when `reload.enabled=true`.
This grants the root container access to privileged host systemd operations,
including starting root processes on the node. Enable it only for trusted images.
For raw manifests, add `--reload`, a `dbus-socket` hostPath volume of type `Socket`
for `/run/dbus/system_bus_socket`, its read-only mount at the same path, and
`DBUS_SYSTEM_BUS_ADDRESS=unix:path=/run/dbus/system_bus_socket` together.

## Edge cases

| Scenario | Handling |
|---|---|
| MetalLB reassigns IP | Update event → entry rewritten |
| Service deleted | Entry removed immediately |
| Node reboots, file wiped | Pod reschedules to same node; startup reconcile rewrites |
| Two Services claim same hostname | Oldest ready Service wins (creation timestamp, then namespace/name to break ties); second is skipped with a log error and Kubernetes Event |
| Annotation added to existing Service | Treated as new entry |
| Annotation removed | Entry removed, same as delete |
| Pod restarts, file already correct | No write; with reload enabled, reload once to recover an interrupted previous reload |
| Managed block markers are incomplete, reversed, or duplicated | Controller emits a Warning Event and leaves the file untouched; no backoff retry. Manual file repairs and subsequent Service events trigger another pass |
| **Manual edit** to hosts file using a rename-based editor (nano, vim, etc.) | The single-file hostPath mount still points at the old inode. The controller detects an unlinked inode and exits with an error. Recreate the pod to bind the replacement file. Prefer in-place edits. |

## Requirements

- `avahi-daemon` installed and running on the designated node
- Node labeled with `avahi-controller: "true"` for pod scheduling
- RBAC: `get/list/watch` on Services (cluster-wide), `create/patch` on Events

## Deployment

### Label the node

This is needed even on a single-node cluster (such as k3s and similar). Without the label, the pod's `nodeSelector` will not match any node and the pod will remain Pending. The controller is intentionally pinned to one node to ensure only one instance manages the hosts file on the node where `avahi-daemon` is running.

Label the node where `avahi-daemon` is running:

```sh
kubectl label node <node-name> avahi-controller=true
```

### Helm

Install the chart into `kube-system`:

```sh
helm repo add avahi-controller https://mm503.github.io/avahi-controller
helm install avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

Or from a local clone:

```sh
helm install avahi-controller ./charts/avahi-controller \
  --namespace kube-system
```

#### Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | At most one writer; `0` disables the deployment |
| `image.repository` | `ghcr.io/mm503/avahi-controller` | Container image repository |
| `image.tag` | `0.6.4` | Pinned release tag; empty falls back to chart appVersion |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `imagePullSecrets` | `[]` | Private registry credentials |
| `nameOverride`, `fullnameOverride` | `""` | Resource naming overrides |
| `healthProbes.enabled` | `false` | Enable HTTP probes with an image supporting `/healthz` and `/readyz`; `0.6.4` lacks these endpoints |
| `reload.enabled` | `false` | Mount privileged host D-Bus access and enable systemd reload |
| `args` | `[--cleanup-on-exit=false]` | Additional flags; configure reload with `reload.enabled` |
| `env` | `NODE_NAME` from node field | Additional environment variables; bus address added only for reload |
| `nodeSelector` | `{avahi-controller: "true"}` | Labels of the node running avahi |
| `tolerations` | `[{operator: Exists}]` | Tolerates any taint on the designated node |
| `affinity` | `{}` | Pod affinity rules |
| `resources.requests` | `cpu: 10m, memory: 32Mi` | Resource requests |
| `resources.limits` | `cpu: 50m, memory: 64Mi` | Resource limits |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `serviceAccount.name` | `""` | Generated name when create is true |
| `serviceAccount.annotations` | `{}` | ServiceAccount annotations |
| `rbac.create` | `true` | Create ClusterRole and ClusterRoleBinding |
| `podAnnotations`, `podLabels` | `{}` | Pod metadata |
| `podSecurityContext` | `{}` | Pod security settings |
| `securityContext` | root, drop ALL, RuntimeDefault seccomp | Writes root-owned file; no privilege escalation; read-only rootfs |
| `volumes`, `volumeMounts` | avahi hosts file hostPath | Hosts file mounts; reload adds D-Bus separately |
| `terminationGracePeriodSeconds` | `30` | Allows in-flight writes and optional cleanup to finish |

The chart defaults to GHCR. To pull from Docker Hub instead, set
`--set image.repository=mm404/avahi-controller`.

Example — enable `--reload` and point at a custom avahi service unit:

```sh
helm install avahi-controller ./charts/avahi-controller \
  --namespace kube-system \
  --set reload.enabled=true \
  --set args="{--avahi-service=avahi.service,--verbose}"
```

#### Upgrade

```sh
helm repo update avahi-controller
helm upgrade avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

### Raw manifests

```sh
kubectl apply -f deploy/
```

## Image

Release images are published to both registries, with tags `latest` and `x.y.z`:

```
ghcr.io/mm503/avahi-controller:<version>
docker.io/mm404/avahi-controller:<version>
```

Builds from feature branches go to a separate package, tagged
`dev-<version>-<branch>-<sha>`:

```
ghcr.io/mm503/avahi-controller-dev:dev-<version>-<branch>-<sha>
```


## Health and repair

The default `0.6.4` image does not expose health endpoints, so the chart and raw
manifest leave probes disabled. With an image built from this code, set
`healthProbes.enabled=true` in Helm. For the raw manifest, add container port 8080
and HTTP readiness/liveness probes for `/readyz` and `/healthz`, respectively;
give liveness an initial delay of 90 seconds.

Cache sync must complete within one minute. `/readyz` and `/healthz` on port 8080
return success after a completed reconcile, including passes with pending IPs.
They fail when the last success is older than `resync-period + 1m`, except that
malformed markers immediately fail readiness while liveness stays successful so
the controller can wait for manual repair without restarting.
Repeated reconciliation failures emit a `ReconcileFailed` Warning Event on the pod.
The controller checks for hosts-file changes every second and reconciles them.
Malformed markers require manual repair; markers inside ordinary comments are
ignored. Remove stray legacy END lines only after checking the static entries.
