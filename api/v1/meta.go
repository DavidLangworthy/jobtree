package v1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// The API types embed the real Kubernetes apimachinery types. Aliases keep
// the engine packages referring to them through this package, so the engine
// stays independent of where the machinery comes from.
type (
	TypeMeta   = metav1.TypeMeta
	ObjectMeta = metav1.ObjectMeta
	ListMeta   = metav1.ListMeta
	Time       = metav1.Time
	Duration   = metav1.Duration

	// RuntimeObject is the serialisable API object contract.
	RuntimeObject = runtime.Object
)

// NewTime constructs a new Time.
func NewTime(t time.Time) Time {
	return metav1.NewTime(t)
}
