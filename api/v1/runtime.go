package v1

// RuntimeObject represents a serialisable API object.
type RuntimeObject interface {
	DeepCopyObject() RuntimeObject
}
