# avahi-controller

A Kubernetes controller that publishes LoadBalancer Service IPs to mDNS by managing a block in `/etc/avahi/hosts` on a designated node.

## How it works

The controller runs as a single pod pinned to one node where `avahi-daemon` is installed. It watches Services cluster-wide and updates a managed block in the node's hosts file whenever the desired state changes. avahi-daemon picks up the change by itself: it watches `/etc/avahi` with inotify and re-reads the hosts file whenever a file there is written, so the chart needs no D-Bus or systemd access. The pod mounts only `/etc/avahi/hosts` and rewrites it in place; if the file is replaced on the host (for example by an editor that saves via rename), restart the pod so it binds to the new file.

This can be particularly useful in combination with MetalLB, which assigns LAN IPs to your Kubernetes LoadBalancers.

> **Note:** This is not a production tool. mDNS is not a production-level solution. This is intended for home labs and simple Kubernetes clusters.

## Prerequisites

- `avahi-daemon` installed and running on the designated node
- Every static line in `/etc/avahi/hosts` must be one IP address and one hostname; avahi stops reading the file at the first invalid line
- The node must be labeled with `avahi-controller: "true"` — **the pod will remain Pending without this label**
- RBAC: `list/watch` on Services (cluster-wide), `create/patch` on Events

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
| `image.repository` | `ghcr.io/mm503/avahi-controller` | Container image repository |
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

Release images are published to both `ghcr.io/mm503/avahi-controller` and
`docker.io/mm404/avahi-controller`. The chart defaults to GHCR; set
`--set image.repository=mm404/avahi-controller` to pull from Docker Hub.

## Controller flags

| Flag | Default | Description |
|---|---|---|
| `--hosts-file` | `/etc/avahi/hosts` | Path to the avahi hosts file |
| `--kubeconfig` | *(in-cluster)* | Path to kubeconfig; empty uses in-cluster config |
| `--cleanup-on-exit` | `false` | Remove managed block from Avahi hosts on controller shutdown |
| `--resync-period` | `10m` | Informer resync interval |
| `--verbose` | `false` | Verbose logging |
| `--debug` | `false` | Debug logging (very noisy) |

Example — enable verbose logging and remove the managed block on shutdown:

```sh
helm install avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system \
  --set args="{--cleanup-on-exit=true,--verbose}"
```

## Upgrade

```sh
helm repo update avahi-controller
helm upgrade avahi-controller avahi-controller/avahi-controller \
  --namespace kube-system
```

The chart is released together with the controller and has the same version
number. A breaking change in either bumps the major version.

### Upgrading from 0.6.x (chart 0.1.x)

The `--reload` and `--avahi-service` flags were removed, along with the host
D-Bus socket mount; avahi-daemon reloads the hosts file on its own. Before
upgrading:

- Remove `--reload` and `--avahi-service` from `args`. The controller now
  exits at startup if either is passed.
- If you set your own `volumes`, `volumeMounts` or `env`, delete the
  `/run/dbus/system_bus_socket` volume and mount and the
  `DBUS_SYSTEM_BUS_ADDRESS` variable. Helm replaces these lists instead of
  merging them, so a custom copy keeps mounting the host D-Bus socket, which
  gives the root container control of systemd on the node.

## License

MIT — see [LICENSE](LICENSE).
