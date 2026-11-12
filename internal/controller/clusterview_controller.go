package controller

import (
	"context"
	"time"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const defaultRefresh = 30 * time.Second

// ClusterViewReconciler reconciles a ClusterView object.
type ClusterViewReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=clusterview.io,resources=clusterviews,verbs=get;list;watch
//+kubebuilder:rbac:groups=clusterview.io,resources=clusterviews/status,verbs=patch
//+kubebuilder:rbac:groups="",resources=nodes,verbs=list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=list;watch

// Reconcile computes the summary and writes it to status.
func (r *ClusterViewReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cv clusterviewv1.ClusterView
	if err := r.Get(ctx, req.NamespacedName, &cv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, err
	}
	var podList corev1.PodList
	if err := r.List(ctx, &podList); err != nil {
		return ctrl.Result{}, err
	}

	base := cv.DeepCopy()
	cv.Status = Aggregate(nodeList.Items, podList.Items, cv.Spec.Namespaces, metav1.Now())
	if err := r.Status().Patch(ctx, &cv, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, err
	}

	interval := cv.Spec.RefreshInterval.Duration
	if interval <= 0 {
		interval = defaultRefresh
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// enqueueAll maps any Node/Pod event to all ClusterView objects.
func (r *ClusterViewReconciler) enqueueAll(ctx context.Context, _ client.Object) []reconcile.Request {
	var list clusterviewv1.ClusterViewList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
	}
	return reqs
}

// SetupWithManager wires the reconciler and its Node/Pod watches.
func (r *ClusterViewReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Only spec changes (generation bumps) re-trigger from the CR
		// itself; our own status patches must not, or every reconcile
		// would enqueue another one.
		For(&clusterviewv1.ClusterView{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAll)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAll)).
		Complete(r)
}
