package controller

import (
	"sort"

	clusterviewv1 "github.com/girishsg24/k8-cluster-view/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Aggregate computes the ClusterView summary from nodes and pods.
// namespaces is an include-list; empty means all. Pure: no I/O.
func Aggregate(nodes []corev1.Node, pods []corev1.Pod, namespaces []string, now metav1.Time) clusterviewv1.ClusterViewStatus {
	nsSet := make(map[string]bool, len(namespaces))
	for _, n := range namespaces {
		nsSet[n] = true
	}
	inScope := func(ns string) bool { return len(nsSet) == 0 || nsSet[ns] }

	status := clusterviewv1.ClusterViewStatus{
		LastUpdated: &now,
		NodeCount:   int32(len(nodes)),
		PodsByPhase: map[string]int32{},
	}
	for i := range nodes {
		if isNodeReady(&nodes[i]) {
			status.ReadyNodes++
		} else {
			status.NotReadyNodes++
		}
	}

	counts := map[string]int32{}
	for i := range pods {
		p := &pods[i]
		if !inScope(p.Namespace) {
			continue
		}
		status.PodCount++
		status.PodsByPhase[string(p.Status.Phase)]++
		if p.Spec.NodeName != "" {
			counts[p.Spec.NodeName]++
		}
	}

	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		status.PodsPerNode = append(status.PodsPerNode, clusterviewv1.PodsPerNode{NodeName: n, PodCount: counts[n]})
	}
	return status
}

func isNodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
