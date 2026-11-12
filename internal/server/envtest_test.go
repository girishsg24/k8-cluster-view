package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	"github.com/girishsg24/k8-cluster-view/internal/controller"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

// TestEnvtest_EndToEndWithLeastPrivilegeRBAC runs the full operator
// (controller + HTTP API) against a real apiserver, authenticated as the
// operator's ServiceAccount bound ONLY to config/rbac/role.yaml. Any
// missing permission surfaces as a failed reconcile or API call.
func TestEnvtest_EndToEndWithLeastPrivilegeRBAC(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping envtest")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	adminCfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterviewv1.AddToScheme(scheme)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	admin, err := client.New(adminCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	// Install the generated ClusterRole and bind it to the operator SA.
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatal(err)
	}
	mustOK(t, admin.Create(ctx, &role))
	mustOK(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "clusterview"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: "clusterview", Namespace: "clusterview"}},
	}))

	// Seed: two nodes (one NotReady), pods in two namespaces, a ClusterView
	// filtered to "default".
	for _, n := range []struct {
		name  string
		ready corev1.ConditionStatus
	}{{"node-a", corev1.ConditionTrue}, {"node-b", corev1.ConditionFalse}} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n.name}}
		mustOK(t, admin.Create(ctx, node))
		node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: n.ready}}
		node.Status.Capacity = corev1.ResourceList{"cpu": mustQty("2"), "memory": mustQty("4Gi")}
		node.Status.Allocatable = corev1.ResourceList{"cpu": mustQty("1900m"), "memory": mustQty("3Gi")}
		mustOK(t, admin.Status().Update(ctx, node))
	}
	mustOK(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other"}}))
	for _, p := range []struct{ ns, name, node string }{
		{"default", "web-1", "node-a"}, {"default", "web-2", "node-a"}, {"other", "x", "node-a"}, {"default", "db", "node-b"},
	} {
		mustOK(t, admin.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: p.ns},
			Spec:       corev1.PodSpec{NodeName: p.node, Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
		}))
	}
	mustOK(t, admin.Create(ctx, &clusterviewv1.ClusterView{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       clusterviewv1.ClusterViewSpec{Namespaces: []string{"default"}},
	}))

	// Operator runs as the ServiceAccount (impersonated), like in-cluster.
	saCfg := rest.CopyConfig(adminCfg)
	saCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:clusterview:clusterview",
		Groups:   []string{"system:serviceaccounts", "system:serviceaccounts:clusterview", "system:authenticated"},
	}
	mgr, err := ctrl.NewManager(saCfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&controller.ClusterViewReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(saCfg)
	if err != nil {
		t.Fatal(err)
	}
	// RBAC really is enforced: the SA must not be able to do anything
	// outside role.yaml.
	if _, err := cs.CoreV1().Secrets("").List(ctx, metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Fatalf("expected Forbidden listing secrets as the SA, got %v", err)
	}
	if err := cs.CoreV1().Pods("default").Delete(ctx, "db", metav1.DeleteOptions{}); !apierrors.IsForbidden(err) {
		t.Fatalf("expected Forbidden deleting a pod as the SA, got %v", err)
	}
	addr := freeAddr(t)
	mustOK(t, mgr.Add(&Server{Addr: addr, Clientset: cs, Reader: mgr.GetAPIReader(), CRName: "cluster"}))
	go func() { _ = mgr.Start(ctx) }()

	base := "http://" + addr
	var summary clusterviewv1.ClusterViewStatus
	eventually(t, 20*time.Second, func() error {
		if err := getJSON(base+"/api/v1/summary", &summary); err != nil {
			return err
		}
		if summary.NodeCount != 2 || summary.ReadyNodes != 1 || summary.NotReadyNodes != 1 || summary.PodCount != 3 {
			return fmt.Errorf("summary not converged: %+v", summary)
		}
		return nil
	})

	var nodes []NodeView
	mustOK(t, getJSON(base+"/api/v1/nodes", &nodes))
	if len(nodes) != 2 {
		t.Fatalf("nodes=%+v", nodes)
	}
	for _, n := range nodes {
		if n.Name == "node-a" && (!n.Ready || n.PodCount != 2 || n.CPUAllocatable != "1900m" || n.MemoryCapacity != "4Gi") {
			t.Fatalf("node-a view wrong: %+v", n)
		}
		if n.Name == "node-b" && (n.Ready || n.PodCount != 1) {
			t.Fatalf("node-b view wrong: %+v", n)
		}
	}

	var pods []PodView
	mustOK(t, getJSON(base+"/api/v1/nodes/node-a/pods", &pods))
	if len(pods) != 2 { // "other/x" is filtered by spec.namespaces
		t.Fatalf("node-a pods=%+v", pods)
	}

	resp, err := http.Get(base + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("UI: err=%v resp=%v", err, resp)
	}
	resp.Body.Close()
}

func getJSON(url string, v any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return fmt.Errorf("%s: %d %v", url, resp.StatusCode, body)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func eventually(t *testing.T, timeout time.Duration, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out: %v", err)
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustQty(s string) resource.Quantity { return resource.MustParse(s) }
