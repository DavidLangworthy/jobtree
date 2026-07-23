// Package v1 contains API Schema definitions for the rq v1 API group.
// +kubebuilder:object:generate=true
// +groupName=rq.davidlangworthy.io
package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion identifies the API group and version for these types.
var GroupVersion = schema.GroupVersion{Group: "rq.davidlangworthy.io", Version: "v1"}

// SchemeGroupVersion is kept as an alias for callers using the older name.
var SchemeGroupVersion = GroupVersion

// SchemeBuilder collects the functions that register this group's types.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme registers the types in this group with a scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion,
		&Run{}, &RunList{},
		&Budget{}, &BudgetList{},
		&GPULease{}, &GPULeaseList{},
		&Reservation{}, &ReservationList{},
	)
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
