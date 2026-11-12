package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) getCV(ctx context.Context) (*clusterviewv1.ClusterView, error) {
	var cv clusterviewv1.ClusterView
	if err := s.Reader.Get(ctx, types.NamespacedName{Name: s.CRName}, &cv); err != nil {
		return nil, err
	}
	return &cv, nil
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	cv, err := s.getCV(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, cv.Status)
}

// NodeCondition is a trimmed node condition for the UI.
type NodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// NodeView is the per-node API shape.
type NodeView struct {
	Name              string          `json:"name"`
	Ready             bool            `json:"ready"`
	Roles             []string        `json:"roles"`
	Conditions        []NodeCondition `json:"conditions"`
	CPUCapacity       string          `json:"cpuCapacity"`
	CPUAllocatable    string          `json:"cpuAllocatable"`
	MemoryCapacity    string          `json:"memoryCapacity"`
	MemoryAllocatable string          `json:"memoryAllocatable"`
	PodCount          int32           `json:"podCount"`
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodeList, err := s.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{ResourceVersion: "0"})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Pod counts come from the CR status (cosmetic). The node list itself
	// is independent of the CR, so a CR-read failure must NOT break the
	// table: degrade to zero counts, but log it rather than swallow it.
	counts := map[string]int32{}
	if cv, err := s.getCV(ctx); err == nil {
		for _, p := range cv.Status.PodsPerNode {
			counts[p.NodeName] = p.PodCount
		}
	} else {
		logf.Log.WithName("api").Error(err, "reading ClusterView for /nodes pod counts; serving zero counts", "clusterView", s.CRName)
	}
	views := make([]NodeView, 0, len(nodeList.Items))
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		views = append(views, nodeToView(n, counts[n.Name]))
	}
	writeJSON(w, http.StatusOK, views)
}

func nodeToView(n *corev1.Node, podCount int32) NodeView {
	v := NodeView{
		Name:              n.Name,
		PodCount:          podCount,
		CPUCapacity:       n.Status.Capacity.Cpu().String(),
		CPUAllocatable:    n.Status.Allocatable.Cpu().String(),
		MemoryCapacity:    n.Status.Capacity.Memory().String(),
		MemoryAllocatable: n.Status.Allocatable.Memory().String(),
		Roles:             []string{},
		Conditions:        []NodeCondition{},
	}
	for _, c := range n.Status.Conditions {
		v.Conditions = append(v.Conditions, NodeCondition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason})
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			v.Ready = true
		}
	}
	for k := range n.Labels {
		if strings.HasPrefix(k, "node-role.kubernetes.io/") {
			v.Roles = append(v.Roles, strings.TrimPrefix(k, "node-role.kubernetes.io/"))
		}
	}
	sort.Strings(v.Roles)
	return v
}

// PodView is the per-pod API shape for the drill-down.
type PodView struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	RestartCount int32  `json:"restartCount"`
	Node         string `json:"node"`
}

func (s *Server) handleNodePods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nodeName := r.PathValue("name")

	// Read the CR to determine the namespace include-list. Treat a read
	// failure as fatal rather than silently widening scope to all
	// namespaces: if we cannot learn the configured scope we must not
	// leak out-of-scope pods, and the drill-down must stay consistent
	// with the namespace-filtered /nodes counts.
	cv, err := s.getCV(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var nsSet map[string]bool
	if len(cv.Spec.Namespaces) > 0 {
		nsSet = make(map[string]bool, len(cv.Spec.Namespaces))
		for _, ns := range cv.Spec.Namespaces {
			nsSet[ns] = true
		}
	}

	// The namespace include-list is a display filter, not a security
	// boundary: we list the node's pods cluster-wide (the operator's RBAC
	// already grants cluster-wide pod list/watch) and filter client-side
	// below so the drill-down matches the namespace-scoped /nodes counts.
	podList, err := s.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector:   "spec.nodeName=" + nodeName,
		ResourceVersion: "0",
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	views := make([]PodView, 0, len(podList.Items))
	for i := range podList.Items {
		p := &podList.Items[i]
		if nsSet != nil && !nsSet[p.Namespace] {
			continue
		}
		views = append(views, podToView(p))
	}
	writeJSON(w, http.StatusOK, views)
}

func podToView(p *corev1.Pod) PodView {
	var restarts int32
	for _, cs := range p.Status.ContainerStatuses {
		restarts += cs.RestartCount
	}
	return PodView{
		Namespace:    p.Namespace,
		Name:         p.Name,
		Phase:        string(p.Status.Phase),
		RestartCount: restarts,
		Node:         p.Spec.NodeName,
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
