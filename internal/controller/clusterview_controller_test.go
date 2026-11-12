package controller

import (
	"context"
	"testing"
	"time"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcile_WritesStatus(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterviewv1.AddToScheme(scheme)

	cv := &clusterviewv1.ClusterView{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
	n := node("a", true)
	p := pod("p1", "default", "a", corev1.PodRunning)

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cv, &n, &p).
		WithStatusSubresource(&clusterviewv1.ClusterView{}).
		Build()

	r := &ClusterViewReconciler{Client: c, Scheme: scheme}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "cluster"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter != 30*time.Second {
		t.Fatalf("requeue=%v", res.RequeueAfter)
	}

	var got clusterviewv1.ClusterView
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cluster"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.NodeCount != 1 || got.Status.ReadyNodes != 1 || got.Status.PodCount != 1 {
		t.Fatalf("status=%+v", got.Status)
	}
}
