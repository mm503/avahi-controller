# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and updates a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change automatically (it watches the file by default).

This can be particularly useful in combination with MetalLB, which assigns LAN IPs to your Kubernetes LoadBalancers.

> **Note:** This is not a production tool. mDNS is not a production-level solution. This is intended for home labs and simple Kubernetes clusters.

## Prerequisites

- `avahi-daemon` installed and running on the designated node
- The node must be labeled with `avahi-controller: "true"` — **the pod will remain Pending without this label**
- RBAC: `get/list/watch` on Services (cluster-wide), `create/patch` on Events

## Node labeling (required)

The controller is intentionally pinned to one node to ensure a single instance manages the hosts file where `avahi-daemon` runs. This is required even on a single-node cluster (k3s, etc.).

Label the node before installing:

```sh
kubectl label node <node-name> avahi-controller=true
```

Without this label, the pod's `nodeSelector` will not match any node and the pod will stay in `Pending` indefinitely.

## Installation

```sh
helm repo add avahi-controller https://mm503.github.io/avahi-controller
helm repo update avahi-controller
helm install avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

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

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | `mm404/avahi-controller` | Container image repository |
| `image.tag` | *(chart appVersion)* | Image tag override |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `args` | `[--cleanup-on-exit=false]` | Controller flags passed to the binary |
| `nodeSelector` | `{avahi-controller: "true"}` | Node selector — must match a labeled node |
| `tolerations` | `[{operator: Exists}]` | Tolerates any taint on the designated node |
| `resources.requests` | `cpu: 10m, memory: 32Mi` | Resource requests |
| `resources.limits` | `cpu: 50m, memory: 64Mi` | Resource limits |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `rbac.create` | `true` | Create ClusterRole and ClusterRoleBinding |
| `terminationGracePeriodSeconds` | `30` | Grace period for pod shutdown |

## Controller flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig; empty uses in-cluster config |
| `--cleanup-on-exit` | `false` | Remove managed block from Avahi hosts on controller shutdown |
| `--reload` | `false` | Signal avahi-daemon via systemd D-Bus after each write |
| `--avahi-service` | `avahi-daemon.service` | systemd unit name (override for distros using `avahi.service`) |
| `--resync-period` | `10m` | Informer resync interval |
| `--verbose` | `false` | Verbose logging |
| `--debug` | `false` | Debug logging (very noisy) |

Enable `--reload` only if your setup requires an explicit D-Bus reload signal. When enabled, the controller calls `systemctl reload <avahi-service>` via the system D-Bus. Requires the D-Bus socket mounted at `/run/dbus/system_bus_socket`.

Example — enable reload with a custom avahi service unit:

```sh
helm install avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system \
  --set args="{--reload=true,--avahi-service=avahi.service,--verbose}"
```

## Upgrade

```sh
helm repo update avahi-controller
helm upgrade avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

## License

MIT — see [LICENSE](LICENSE).
