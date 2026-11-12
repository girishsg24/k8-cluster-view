# ClusterView

A Kubernetes operator that watches Nodes and Pods, writes an aggregated
summary into a `ClusterView` custom resource's `status`, and serves an
HTTP API + embedded web UI visualizing cluster state (nodes, their
status/conditions, CPU/memory capacity vs. allocatable, and the pods on
each node with phase and restart count). Click a node to see its pods.

## Quick install (published image)

A prebuilt image is published at
`ghcr.io/girishsg24/k8-cluster-view:0.1.0` (public, linux/amd64), so no
build step is needed:

```bash
helm install clusterview deploy/helm/clusterview -n clusterview --create-namespace \
  --set image.repository=ghcr.io/girishsg24/k8-cluster-view --set image.tag=0.1.0
```

```console
$ kubectl get cv
NAME      NODES   READY   PODS   AGE
cluster   3       3       19     2m17s
```

Then open the UI with
`kubectl -n clusterview port-forward deploy/clusterview 8082:8082` and
browse to http://localhost:8082 (API under `/api/v1/summary`,
`/api/v1/nodes`, `/api/v1/nodes/{name}/pods`). To expose it publicly
instead, see [Reach the UI](#reach-the-ui).

## Architecture

Single binary, single process. One controller-runtime `Manager` runs:

- **Reconciler** (informer-cache backed): watches `ClusterView`, `Node`,
  `Pod`; computes the summary; writes it to `ClusterView.status`. This is
  the only component that uses the cache.
- **HTTP server** (`Runnable`, port 8082): serves the API and UI using a
  **client-go clientset** for near-real-time, field-selected Node/Pod
  queries (`ResourceVersion=0`, served from the apiserver watch cache)
  and the Manager's uncached `GetAPIReader()` to read the `ClusterView`
  CR. It never reads the informer cache.

Why this split: the O(all-pods) aggregation runs in the reconciler (once
per reconcile, in memory) and is persisted to `status`; the API stays
cheap - `/summary` is O(1), `/nodes` is O(nodes) (counts come from
`status`), and the per-node drill-down is a `spec.nodeName`-filtered,
watch-cache-served query (`ResourceVersion=0`). See
`docs/superpowers/specs/2026-11-11-clusterview-operator-design.md`.

### API

- `GET /api/v1/summary` - aggregated summary (from `status`).
- `GET /api/v1/nodes` - node list with conditions, capacity/allocatable,
  pod counts.
- `GET /api/v1/nodes/{name}/pods` - pods on a node (namespace, phase,
  restart count).
- `GET /healthz`, `GET /readyz`, `GET /` (UI).

## Prerequisites

Go 1.25+, Docker, `kubectl`, Helm 3, and `controller-gen`
(`go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest`).

## Local development

```bash
make generate     # deepcopy + CRD + RBAC (regenerate after type changes)
make test         # unit tests (envtest tests skip themselves)
make test-envtest # + controller and end-to-end API/RBAC tests on a real apiserver
make build        # build ./bin/manager
make vet
```

`make test-envtest` downloads a kube-apiserver + etcd via `setup-envtest`
and runs:

- `internal/controller/envtest_test.go`: the real controller wiring
  (watches, predicates, status subresource + CRD schema validation);
  checks status converges, isn't rewritten while idle, and reacts to Pod
  events.
- `internal/server/envtest_test.go`: the full operator (controller + HTTP
  API) running as the operator ServiceAccount bound **only** to
  `config/rbac/role.yaml`, hitting every endpoint, and asserting that
  out-of-scope actions (listing Secrets, deleting Pods) are Forbidden.

Run against a cluster your `kubeconfig` points at (needs the CRD
installed and a ClusterView instance; `--clusterview-name` must match):

```bash
kubectl apply -f config/crd/bases/clusterview.io_clusterviews.yaml
kubectl apply -f - <<'EOF'
apiVersion: clusterview.io/v1
kind: ClusterView
metadata: { name: cluster }
spec: { refreshInterval: 30s }
EOF
go run ./cmd/manager --clusterview-name=cluster
# open http://localhost:8082
```

## Build & deploy

The cluster pulls the image from a registry, so push it to one the
cluster can reach. The nodes are `linux/amd64`; the Makefile
cross-builds for that by default (`PLATFORM=linux/amd64`), so this works
from an Apple-silicon Mac too.

```bash
# Any public registry works, e.g. GHCR or Docker Hub:
export IMG=ghcr.io/<you>/clusterview:0.1.0
docker login ghcr.io
make docker-push IMG=$IMG

helm install clusterview deploy/helm/clusterview \
  -n clusterview --create-namespace \
  --set image.repository=${IMG%:*} --set image.tag=${IMG##*:}

kubectl -n clusterview rollout status deploy/clusterview
kubectl get cv            # NODES / READY / PODS columns fill in
```

### Reach the UI

Port-forward (default `ClusterIP`):

```bash
kubectl -n clusterview port-forward deploy/clusterview 8082:8082
# open http://localhost:8082
```

Or expose publicly:

```bash
helm upgrade clusterview deploy/helm/clusterview -n clusterview --reuse-values \
  --set service.type=LoadBalancer
kubectl -n clusterview get svc clusterview -w   # wait for EXTERNAL-IP, then open http://<EXTERNAL-IP>:8082
```

### Configure what to observe

```bash
helm upgrade clusterview deploy/helm/clusterview -n clusterview --reuse-values \
  --set 'clusterView.namespaces={default,kube-system}' \
  --set clusterView.refreshInterval=15s
```

## RBAC (least privilege)

The operator is granted only: `nodes` and `pods` `list,watch`;
`clusterviews` `get,list,watch`; `clusterviews/status` `patch`. No write
access to core resources, no leader-election/events permissions. RBAC is
generated from `+kubebuilder:rbac` markers in the reconciler
(`make generate` -> `config/rbac/role.yaml`); the chart's ClusterRole
mirrors it.

## Design decisions

- **Summary in `status`, detail live** - the CR status is the shared,
  `kubectl get cv`-visible summary; per-node/per-pod detail is served
  live and is intentionally not stored in `status`.
- **No serving cache / no snapshot** - the API does targeted,
  field-selected API queries instead of caching all pods in-process.
- **Trimmed scope** - no leader election (single replica), no metrics
  endpoint, no status conditions. See the spec for the full rationale.

## Caveats

- Helm installs CRDs from `crds/` once; it does not upgrade/delete them
  on `helm upgrade`/`uninstall` - update the CRD manually if the schema
  changes.
- The bundled `ClusterView` instance is applied as a
  `post-install,post-upgrade` Helm hook so it lands only after the CRD is
  established (avoiding a cold-install race). As a hook it is not tracked
  as a normal release resource: `helm uninstall` leaves it in place
  (delete it with `kubectl delete clusterview <name>`), and each
  `helm upgrade` recreates it (`before-hook-creation` delete policy), so
  its status is transiently reset and recomputed within `refreshInterval`.
- `/summary` and `/nodes` counts lag by up to `refreshInterval`; the
  per-node drill-down is near-real-time (watch-cache served).
