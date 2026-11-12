# ClusterView Operator — Design

**Date:** 2026-11-11
**Status:** Approved (brainstorming) — pending implementation plan

## 1. Goal

A Kubernetes operator, written in Go with controller-runtime, that:

1. Defines a `ClusterView` CRD whose spec configures what to observe (a
   namespace filter and a refresh interval).
2. Watches `Node` and `Pod` objects and writes an aggregated summary
   (node count, ready/not-ready nodes, pods per node, pod phases) into
   the CR's `status`.
3. Exposes an HTTP API serving richer cluster state (node list, node
   status/conditions, CPU/memory capacity vs. allocatable, pods per
   node with phase and restart count) and a simple embedded frontend
   that visualizes it. Clicking a node shows the pods running on it.
4. Ships a Dockerfile and a Helm chart (CRD + `ClusterView` instance +
   least-privilege RBAC), with the frontend reachable via port-forward
   (default) or a `LoadBalancer` Service (toggle).
5. Ships a README with local dev setup, build/deploy steps, and an
   architecture/design-decisions explanation.

## 2. Architecture

**Single binary, single process (chosen).** One controller-runtime
`Manager` runs both:

- the **`ClusterView` reconciler**, and
- an **HTTP server** registered as a manager `Runnable`.

They share the process and the Manager's `*rest.Config`, but **not a
cache** (see §2 data flow): the reconciler reads the informer cache; the
server uses direct, uncached reads. One Deployment, one container, one
ServiceAccount / RBAC identity.

Rejected alternatives:

- **Two binaries** (operator + separate API server) — doubles
  Deployments, RBAC identities, and informer setup for no benefit at
  this scope.
- **Operator-only, web pod reads just the CR** — forfeits the direct
  per-node / per-pod detail (conditions, restart counts) the task
  requires, since that detail never lives in `status`.

### Data flow (reconciler and REST are decoupled)

The reconciler and the HTTP server do **not** share a cache. They use
two independent access paths, which is what keeps the API cheap at
scale:

- **Reconciler (cache-backed):** the controller-runtime Manager's
  informers (Node/Pod/ClusterView) keep a cache current via watches.
  The reconciler reads that cache, computes the **aggregated summary**
  (counts, phase tallies, pods-per-node), and writes it to
  **`ClusterView.status`** (small, refresh-interval driven). This is
  the *only* component that uses the cache, and the *only* place an
  O(all-pods) pass happens — once per reconcile, from memory. Reconciles
  are triggered by Node/Pod watch events (coalesced by the workqueue)
  and at least every `refreshInterval`; each pass is an in-memory scan,
  so running more often under churn is cheap (see §2 scale analysis).
- **HTTP server (direct, uncached reads):** holds a **client-go
  clientset** (built from the Manager's `*rest.Config`) for Node/Pod
  queries, and the Manager's **`GetAPIReader()`** (an *uncached* direct
  reader that honors the scheme) for reading the `ClusterView` CR. A
  typed clientset cannot read a CRD, so `GetAPIReader()` — not the
  clientset and not the cache — serves `/summary`. Each request is a
  **targeted, server-side-filtered** call, so no handler lists all pods:
  - `/summary` → `GetAPIReader().Get(ClusterView)` → returns its
    `status` (O(1)).
  - `/nodes` → `Nodes().List()` and attaches per-node pod counts from
    `status.podsPerNode` (**precomputed**, not a live pod scan).
  - `/nodes/{name}/pods` → `Pods().List(fieldSelector=spec.nodeName=X,
    ResourceVersion="0")`, served from the apiserver watch cache,
    returning only that node's pods (tens, not thousands), then
    filtered client-side by `spec.Namespaces` — same namespace scope as
    the `/nodes` count (may differ transiently by up to `refreshInterval`
    due to the status-vs-direct freshness skew; see Staleness below).

No materialized snapshot, atomic pointer, projector, or field indexer
is needed (all explicitly rejected as over-engineering). The answer to
"won't serving from a cache be slow / overload it" is: **don't serve
from an in-process cache, and never scan all pods per request** —
precompute the summary in `status`, and make the only direct pod query a
filtered, watch-cache-served (bounded-stale), per-node drill-down.

```
 Node/Pod/ClusterView informers (cache)        client-go clientset
            │                                         │ (direct, field-selected)
            ▼                                         ▼
     ClusterView reconciler                      HTTP server
            │                              /nodes (+counts from status),
            ▼                              /nodes/{n}/pods (field-selected)
     ClusterView.status  ─────────────────▶  /summary (reads status)
      (summary, counts)
```

### Scale analysis (~3,000 pods / 100 nodes)

| Endpoint | Work per request | Cost |
|---|---|---|
| `/api/v1/summary` | read one CR (`status`) | O(1) |
| `/api/v1/nodes` | list nodes + counts from `status` | O(nodes) |
| `/api/v1/nodes/{name}/pods` | field-selected pod list, cache-served | O(pods-on-node) |

The only O(all-pods) work is the reconciler's aggregation, an in-memory
scan (sub-millisecond for a few thousand pods). It runs per reconcile —
on coalesced Node/Pod watch events and at least every `refreshInterval`
— so under heavy pod churn it may run more than once per interval; since
each pass is an in-memory scan, that is still cheap. If ever needed, a
controller rate-limiter (or a "skip if re-aggregated within N seconds"
guard) can bound the frequency; not required at this scale.

Residual costs at that scale: reconciler **memory** for the pod informer
(a few MB of *trimmed* pods via a cache `TransformFunc`, bounded by
namespace scoping). **TransformFunc invariant:** trimming must retain
`metadata.{name,namespace,resourceVersion}`, `spec.nodeName`, and
`status.phase` — everything the summary needs. (Restart counts are *not*
in the summary; they come only from the drill-down, which reads full pod
objects via the direct clientset, so trimming the informer cache never
starves it.)

Staleness: `/summary` and `/nodes` counts are up to `refreshInterval`
old; the per-node drill-down is served from the apiserver watch cache
(`RV="0"`) — near-real-time with a small bounded lag, **not** strongly
consistent (dropping `RV="0"` would force an etcd quorum range-read +
apiserver-side filter, i.e. the O(all-pods) path we avoid).

## 3. Components / repo layout

Neutral, standalone project. Module path: `github.com/girishsg24/k8-cluster-view`.
Layout borrows **generic kubebuilder conventions** (one API package with
`groupversion_info.go` + `*_types.go` + generated deepcopy, inline RBAC
markers, `controller-gen` codegen) — no organization-specific names.

```
clusterview/
├── api/v1/
│   ├── groupversion_info.go      # groupName, GroupVersion, SchemeBuilder, AddToScheme
│   ├── clusterview_types.go      # ClusterView{,List}, Spec, Status (+kubebuilder markers)
│   └── zz_generated.deepcopy.go  # generated (controller-gen object)
├── internal/
│   ├── controller/
│   │   ├── clusterview_controller.go  # Reconcile + watches Node/Pod
│   │   └── aggregate.go               # PURE summary function (unit-tested)
│   └── server/
│       ├── server.go             # manager Runnable; routes; client-go clientset (nodes/pods) + mgr.GetAPIReader() (CR)
│       ├── handlers.go           # /api/v1/summary, /nodes, /nodes/{name}/pods, /healthz, /readyz
│       └── web/                  # go:embed: index.html, app.js, style.css
├── cmd/manager/main.go           # wires Manager + reconciler + server Runnable
├── config/
│   ├── crd/bases/                # generated CRD (controller-gen crd)
│   └── rbac/role.yaml            # generated ClusterRole from inline markers
├── deploy/helm/clusterview/      # Helm chart (consumes generated CRD + role)
│   ├── crds/                     # CRD (installed before templates)
│   └── templates/                # Deployment, SA, ClusterRole(+Binding), Service, ClusterView CR
├── hack/boilerplate.go.txt       # codegen header
├── Dockerfile
├── Makefile                      # generate, build, test, docker, helm targets
└── README.md
```

## 4. CRD: `ClusterView` (group `clusterview.io`, version `v1`)

- **Scope:** Cluster. **Kind:** `ClusterView`. Markers on the type:
  `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`,
  `+kubebuilder:resource:scope=Cluster`, and printer columns
  NODES / READY / PODS / AGE (JSONPath into `.status.*`, valid with the
  status subresource). The `30s` default for `RefreshInterval` is
  enforced in code (a kubebuilder default on `metav1.Duration` is
  awkward), not via a marker.
- The inline `+kubebuilder:rbac` markers (§7) set an explicit
  `roleName=clusterview-role` so the generated `ClusterRole` has a
  stable name the Helm `ClusterRoleBinding` references (avoids the
  implicit `manager-role` default drifting out of sync).

```go
type ClusterViewSpec struct {
    // Namespaces is an include-list of namespaces to observe for pods.
    // Empty means all namespaces. (The "namespace filter".)
    // +optional
    Namespaces []string `json:"namespaces,omitempty"`

    // RefreshInterval is how often the summary in status is recomputed.
    // Defaults to 30s when unset/zero.
    // +optional
    RefreshInterval metav1.Duration `json:"refreshInterval,omitempty"`
}

type PodsPerNode struct {
    NodeName string `json:"nodeName"`
    PodCount int32  `json:"podCount"`
}

type ClusterViewStatus struct {
    // +optional
    LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
    NodeCount     int32 `json:"nodeCount"`
    ReadyNodes    int32 `json:"readyNodes"`
    NotReadyNodes int32 `json:"notReadyNodes"`
    PodCount      int32 `json:"podCount"`
    // PodsByPhase maps pod phase (Pending/Running/Succeeded/Failed/Unknown) to count.
    // +optional
    PodsByPhase map[string]int32 `json:"podsByPhase,omitempty"`
    // +optional
    PodsPerNode []PodsPerNode `json:"podsPerNode,omitempty"`
}
```

No `conditions` (trimmed — the task asks for a summary, not condition
plumbing).

## 5. Controller (cache-backed — the only cache user)

- `Reconcile` on `ClusterView`; `Watches` `Node` and `Pod`, mapping
  their events to enqueue the affected `ClusterView`(s) via
  `EnqueueRequestsFromMapFunc`. Watch-driven, with the workqueue
  coalescing duplicate enqueues.
- Each reconcile: list nodes (cluster-wide) + pods (scoped by
  `spec.Namespaces`) from the cache, call the **pure `aggregate()`**
  function, patch `status`, and requeue after `RefreshInterval`
  (default 30s).
- No writes to Nodes/Pods — read-only on core resources. No
  `EventRecorder` is constructed (keeps `events` out of RBAC).
- **Optional pod-cache scoping / trimming:** if the configured namespace
  filter is static, the Manager cache can be limited to those namespaces
  (`cache.Options.DefaultNamespaces`) and pods trimmed with a
  `TransformFunc` that retains the invariant fields from §2. Not
  required at assignment scale; noted for large clusters.

## 6. HTTP API + frontend (port `8082`, direct API queries)

Handlers hold a **client-go clientset** (Node/Pod queries, from the
Manager's `*rest.Config`) and the Manager's **`GetAPIReader()`**
(uncached) for the `ClusterView` CR. Each request is a small, targeted,
server-side-filtered call; nothing is served from the reconciler's
cache.

- `GET /api/v1/summary` — `GetAPIReader().Get(ClusterView)` → returns
  its `status` (the reconciler's precomputed summary). Typed clientsets
  can't read CRDs, which is why this uses `GetAPIReader()`.
- `GET /api/v1/nodes` — `Nodes().List()` (cluster-wide) plus one
  `GetAPIReader().Get(clusterviews)` → per node: name, ready/status,
  roles, conditions, CPU/memory **capacity vs. allocatable** (read from
  `Node.status.{capacity,allocatable}` — no metrics-server needed), and
  pod count joined from `status.podsPerNode` (**precomputed — no live
  pod scan**). Nodes absent from `podsPerNode` render a count of `0`
  (not dropped).
- `GET /api/v1/nodes/{name}/pods` — `Pods(All).List()` with
  `fieldSelector=spec.nodeName=<name>` and `ResourceVersion="0"` →
  full pod objects (namespace, name, phase, **restart count**), then
  **filtered client-side by `spec.Namespaces`** (same namespace scope as
  the `/nodes` count; may differ transiently due to the freshness skew
  in §2). Watch-cache served, never a full pod list.
- `GET /healthz`, `GET /readyz` — `/readyz` additionally gates on the
  Manager cache being synced (`WaitForCacheSync`), so readiness waits
  until the **reconciler** has had a chance to populate `status` — the
  uncached summary path itself has no informer dependency.
- `GET /` — embedded UI (`go:embed`): summary header + pod-phase
  breakdown, node table, **click a node → its pods**. Auto-refresh on
  an interval. Plain HTML/JS/CSS, no build step. Served via
  `fs.Sub(embedded, "web")` + `http.FileServer`, with the longer
  `/api/v1/…` mux patterns taking precedence over the `/` catch-all.

Pod scope for `/summary` is the designated `ClusterView` (name via flag/
env, default matches the deployed instance — the single Helm-installed
CR); node data is cluster-wide.

## 7. Deployment

**Helm chart** `deploy/helm/clusterview`:

- `crds/` CRD (applied before templates). Caveat: Helm installs
  `crds/` once and does **not** upgrade or delete it on
  `helm upgrade`/`uninstall`; README notes manual CRD updates.
- `templates/`: Deployment (1 replica, non-root, `runAsNonRoot`,
  read-only rootfs, probes on `/healthz` `/readyz`), ServiceAccount,
  ClusterRole + ClusterRoleBinding (binding references the generated
  `clusterview-role` from `role.yaml`),
  Service, and a `ClusterView` instance.
- `values.yaml`: `image.repository/tag`, `service.type`
  (`ClusterIP` default; `LoadBalancer` toggle), `service.port`,
  `clusterView.namespaces`, `clusterView.refreshInterval`, `resources`.

**Exposure:** `ClusterIP` + documented `kubectl port-forward` by
default; set `service.type=LoadBalancer` for a public endpoint.

### RBAC (least privilege, generated from inline markers)

Single source of truth = `+kubebuilder:rbac` markers (with
`roleName=clusterview-role`) on the reconciler; `controller-gen` emits
`config/rbac/role.yaml` as `ClusterRole/clusterview-role`:

- `""` (core) `nodes`: `list,watch`
- `""` (core) `pods`: `list,watch`
- `clusterview.io` `clusterviews`: `get,list,watch`
- `clusterview.io` `clusterviews/status`: `patch`

(No single-object `get` on nodes/pods — the reconciler and informers
`list`/`watch`, and the HTTP server only `list`s — so `get` is omitted
as an unused grant. `clusterviews` keeps `get` for the server's
`GetAPIReader().Get(...)` plus `list,watch` for the reconciler's
informer. The reconciler writes status via `.Status().Patch()`, so
`clusterviews/status` needs only `patch`.)

No leader-election (`leases`) or `events` rules — single replica and no
`EventRecorder` (§5). No write access to any core resource.

## 8. Containerization

Multi-stage `Dockerfile`: `CGO_ENABLED=0` static build on `golang:1.25`
→ `gcr.io/distroless/static:nonroot`. Runs as non-root, exposes `8082`.

## 9. Trimmed scope (explicit YAGNI cuts)

- **Leader election** — dropped (single replica); removes `leases`/
  `events` RBAC.
- **Metrics endpoint** — disabled; smaller surface.
- **Status `conditions`** — dropped; status is exactly the required
  summary + `lastUpdated`.
- **Materialized snapshot / atomic pointer / debounced projector /
  field indexer** — dropped. The API does direct, field-selected API
  queries instead of serving from an in-process cache, so none of that
  machinery is needed.
- **`shortName`, extra validation enums, boilerplate polish** — skipped
  (kept only `printcolumn`s, which are free).

## 10. Verification

- `go build ./...`, `go vet ./...`, `go test ./...`.
  - **TDD** on the pure `aggregate()` function (node ready/not-ready,
    phase tallies, per-node counts, namespace filtering).
  - Reconciler test with a controller-runtime **fake client**.
  - HTTP handler tests via `httptest` + client-go **fake clientset**
    (`k8s.io/client-go/kubernetes/fake`), asserting the `spec.nodeName`
    field selector is applied for the node→pods endpoint.
- `controller-gen` produces deepcopy + CRD; CRD applies cleanly.
- `helm lint` + `helm template` render without error.
- `docker build` succeeds.
- envtest (`make test-envtest`): the controller against a real
  kube-apiserver (status converges, CRD schema accepts the status patch,
  Pod events drive reconciles), and the full operator running as its
  ServiceAccount bound only to `config/rbac/role.yaml`, hitting every
  API endpoint and asserting out-of-scope actions are Forbidden.
- End-to-end on a real cluster: `helm install`,
  confirm CR status populates and the UI serves node→pod data.

## 11. Delivery

Source on GitHub, image published to
`ghcr.io/girishsg24/k8-cluster-view:0.1.0` (linux/amd64), installable with a single `helm install`
(see the README's "Quick install" section). README documents local
dev, build, deploy, and the architecture/decisions above.
