package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ktesting "k8s.io/client-go/testing"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testServer(objs ...runtime.Object) *Server {
	cs := fake.NewSimpleClientset(objs...)
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterviewv1.AddToScheme(scheme)
	cv := &clusterviewv1.ClusterView{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       clusterviewv1.ClusterViewSpec{Namespaces: []string{"default"}},
		Status: clusterviewv1.ClusterViewStatus{
			NodeCount:   1,
			PodsPerNode: []clusterviewv1.PodsPerNode{{NodeName: "a", PodCount: 2}},
		},
	}
	reader := ctrlfake.NewClientBuilder().WithScheme(scheme).WithObjects(cv).Build()
	return &Server{Clientset: cs, Reader: reader, CRName: "cluster"}
}

func TestHandleSummary(t *testing.T) {
	s := testServer()
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/summary", nil))
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	var st clusterviewv1.ClusterViewStatus
	_ = json.NewDecoder(w.Body).Decode(&st)
	if st.NodeCount != 1 {
		t.Fatalf("nodeCount=%d", st.NodeCount)
	}
}

func TestHandleNodes(t *testing.T) {
	nodeA := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   "a",
		Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
	}}
	nodeA.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	s := testServer(nodeA)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nodes", nil))
	var views []NodeView
	_ = json.NewDecoder(w.Body).Decode(&views)
	if len(views) != 1 || !views[0].Ready || views[0].PodCount != 2 || len(views[0].Roles) != 1 || views[0].Roles[0] != "control-plane" {
		t.Fatalf("views=%+v", views)
	}
}

func TestHandleNodePods_FieldSelectorAndNamespaceFilter(t *testing.T) {
	p1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "a"},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{RestartCount: 3}},
		},
	}
	p2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p2", Namespace: "kube-system"},
		Spec:       corev1.PodSpec{NodeName: "a"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	s := testServer(p1, p2)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nodes/a/pods", nil))

	var views []PodView
	_ = json.NewDecoder(w.Body).Decode(&views)
	// Namespace filter (spec.Namespaces=[default]) drops p2.
	if len(views) != 1 || views[0].Name != "p1" || views[0].RestartCount != 3 {
		t.Fatalf("views=%+v", views)
	}
	// Assert the spec.nodeName field selector was passed to the clientset.
	found := false
	for _, a := range s.Clientset.(*fake.Clientset).Actions() {
		if a.GetVerb() == "list" && a.GetResource().Resource == "pods" {
			if la, ok := a.(ktesting.ListActionImpl); ok && la.GetListRestrictions().Fields.String() == "spec.nodeName=a" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("spec.nodeName field selector not applied")
	}
}

// TestHandleNodePods_CRReadFailureIsFatal verifies the drill-down does not
// fail open: if the ClusterView CR cannot be read, scope is unknown, so the
// handler must return 500 rather than listing pods from all namespaces.
func TestHandleNodePods_CRReadFailureIsFatal(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterviewv1.AddToScheme(scheme)
	// Reader has no ClusterView object -> getCV returns NotFound.
	reader := ctrlfake.NewClientBuilder().WithScheme(scheme).Build()
	s := &Server{Clientset: fake.NewSimpleClientset(), Reader: reader, CRName: "cluster"}

	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nodes/a/pods", nil))
	if w.Code != 500 {
		t.Fatalf("expected 500 on CR-read failure, got %d", w.Code)
	}
	// It must not have listed pods at all once the CR read failed.
	for _, a := range s.Clientset.(*fake.Clientset).Actions() {
		if a.GetVerb() == "list" && a.GetResource().Resource == "pods" {
			t.Fatal("pods were listed despite CR-read failure")
		}
	}
}
