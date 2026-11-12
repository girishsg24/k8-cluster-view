package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// TestEnvtest_ReconcileLoop runs the real controller wiring (watches,
// predicates, status subresource) against a real kube-apiserver + etcd.
// It is skipped unless KUBEBUILDER_ASSETS points at envtest binaries:
//
//	KUBEBUILDER_ASSETS=$(setup-envtest use 1.31.x -p path) go test ./...
func TestEnvtest_ReconcileLoop(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping envtest")
	}

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterviewv1.AddToScheme(scheme)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&ClusterViewReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	// A Ready node (status must be set through the status subresource).
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}
	must(t, c.Create(ctx, n))
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	must(t, c.Status().Update(ctx, n))

	newPod := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName:   "node-a",
				Containers: []corev1.Container{{Name: "c", Image: "busybox"}},
			},
		}
	}
	must(t, c.Create(ctx, newPod("p1")))

	cv := &clusterviewv1.ClusterView{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       clusterviewv1.ClusterViewSpec{RefreshInterval: metav1.Duration{Duration: time.Hour}},
	}
	must(t, c.Create(ctx, cv))

	key := types.NamespacedName{Name: "cluster"}
	waitFor(t, 10*time.Second, func() bool {
		var got clusterviewv1.ClusterView
		return c.Get(ctx, key, &got) == nil &&
			got.Status.NodeCount == 1 && got.Status.ReadyNodes == 1 && got.Status.PodCount == 1
	})

	// Steady state: with nothing changing in the cluster and a 1h refresh
	// interval, the controller must not keep rewriting status (which would
	// mean it is re-triggering itself off its own status writes).
	var before clusterviewv1.ClusterView
	must(t, c.Get(ctx, key, &before))
	time.Sleep(3500 * time.Millisecond)
	var after clusterviewv1.ClusterView
	must(t, c.Get(ctx, key, &after))
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatalf("status rewritten while idle (rv %s -> %s): reconcile is self-triggering",
			before.ResourceVersion, after.ResourceVersion)
	}

	// A Pod watch event must still drive a reconcile promptly.
	must(t, c.Create(ctx, newPod("p2")))
	waitFor(t, 10*time.Second, func() bool {
		var got clusterviewv1.ClusterView
		return c.Get(ctx, key, &got) == nil && got.Status.PodCount == 2
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
