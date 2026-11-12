package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func node(name string, ready bool) corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}},
		},
	}
}

func pod(name, ns, nodeName string, phase corev1.PodPhase) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func TestAggregate_NodeReadiness(t *testing.T) {
	s := Aggregate([]corev1.Node{node("a", true), node("b", false), node("c", true)}, nil, nil, metav1.Now())
	if s.NodeCount != 3 || s.ReadyNodes != 2 || s.NotReadyNodes != 1 {
		t.Fatalf("got nodeCount=%d ready=%d notReady=%d", s.NodeCount, s.ReadyNodes, s.NotReadyNodes)
	}
}

func TestAggregate_PodPhasesAndPerNode(t *testing.T) {
	pods := []corev1.Pod{
		pod("p1", "default", "a", corev1.PodRunning),
		pod("p2", "default", "a", corev1.PodPending),
		pod("p3", "kube-system", "b", corev1.PodRunning),
		pod("unsched", "default", "", corev1.PodPending),
	}
	s := Aggregate(nil, pods, nil, metav1.Now())
	if s.PodCount != 4 {
		t.Fatalf("podCount=%d", s.PodCount)
	}
	if s.PodsByPhase["Running"] != 2 || s.PodsByPhase["Pending"] != 2 {
		t.Fatalf("phases=%v", s.PodsByPhase)
	}
	if len(s.PodsPerNode) != 2 || s.PodsPerNode[0].NodeName != "a" || s.PodsPerNode[0].PodCount != 2 {
		t.Fatalf("perNode=%v", s.PodsPerNode)
	}
}

func TestAggregate_NamespaceFilter(t *testing.T) {
	pods := []corev1.Pod{
		pod("p1", "default", "a", corev1.PodRunning),
		pod("p2", "kube-system", "b", corev1.PodRunning),
	}
	s := Aggregate(nil, pods, []string{"default"}, metav1.Now())
	if s.PodCount != 1 || s.PodsByPhase["Running"] != 1 {
		t.Fatalf("filtered podCount=%d phases=%v", s.PodCount, s.PodsByPhase)
	}
	if len(s.PodsPerNode) != 1 || s.PodsPerNode[0].NodeName != "a" {
		t.Fatalf("perNode=%v", s.PodsPerNode)
	}
}
