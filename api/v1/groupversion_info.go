// Package v1 contains the ClusterView API (group clusterview.io).
// +kubebuilder:object:generate=true
// +groupName=clusterview.io
package v1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group/version for this API.
	GroupVersion = schema.GroupVersion{Group: "clusterview.io", Version: "v1"}
	// SchemeBuilder registers the API types.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	// AddToScheme adds these types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
