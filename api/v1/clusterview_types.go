package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterViewSpec configures what to observe.
type ClusterViewSpec struct {
	// Namespaces is an include-list of namespaces to observe for pods.
	// Empty means all namespaces.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// RefreshInterval is how often the summary is recomputed.
	// Defaults to 30s when unset/zero (enforced in code).
	// +optional
	RefreshInterval metav1.Duration `json:"refreshInterval,omitempty"`
}

// PodsPerNode is the pod count for one node.
type PodsPerNode struct {
	NodeName string `json:"nodeName"`
	// +optional
	PodCount int32 `json:"podCount"`
}

// ClusterViewStatus is the aggregated summary. All fields are optional:
// a freshly created ClusterView has no status, and the controller writes
// it with a merge patch that omits fields whose value did not change.
type ClusterViewStatus struct {
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`
	// +optional
	NodeCount int32 `json:"nodeCount"`
	// +optional
	ReadyNodes int32 `json:"readyNodes"`
	// +optional
	NotReadyNodes int32 `json:"notReadyNodes"`
	// +optional
	PodCount int32 `json:"podCount"`
	// PodsByPhase maps pod phase to count.
	// +optional
	PodsByPhase map[string]int32 `json:"podsByPhase,omitempty"`
	// +optional
	PodsPerNode []PodsPerNode `json:"podsPerNode,omitempty"`
}

// ClusterView is the Schema for the clusterviews API.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cv
// +kubebuilder:printcolumn:name="Nodes",type=integer,JSONPath=`.status.nodeCount`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyNodes`
// +kubebuilder:printcolumn:name="Pods",type=integer,JSONPath=`.status.podCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ClusterView struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec ClusterViewSpec `json:"spec,omitempty"`
	// +optional
	Status ClusterViewStatus `json:"status,omitempty"`
}

// ClusterViewList contains a list of ClusterView.
// +kubebuilder:object:root=true
type ClusterViewList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterView `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterView{}, &ClusterViewList{})
}
