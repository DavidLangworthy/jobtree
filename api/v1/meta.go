package v1

import "time"

// TypeMeta mimics the Kubernetes TypeMeta for documentation compatibility.
type TypeMeta struct {
	Kind       string `json:"kind,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// ObjectMeta mimics a subset of Kubernetes ObjectMeta.
type ObjectMeta struct {
	Name      string            `json:"name,omitempty"`
	Namespace string            `json:"namespace,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// ListMeta mimics the Kubernetes ListMeta.
type ListMeta struct {
	Continue string `json:"continue,omitempty"`
}

// Time wraps time.Time to provide helpers.
type Time struct {
	time.Time
}

// NewTime constructs a new Time.
func NewTime(t time.Time) Time {
	return Time{Time: t}
}

// DeepCopy returns a copy of the time value.
func (t Time) DeepCopy() Time {
	return Time{Time: t.Time}
}

// After reports whether t is after other.
func (t Time) After(other Time) bool {
	return t.Time.After(other.Time)
}

// Sub returns the duration between two Time values.
func (t Time) Sub(other Time) time.Duration {
	return t.Time.Sub(other.Time)
}

// IsZero reports whether the time is zero.
func (t Time) IsZero() bool {
	return t.Time.IsZero()
}

// Duration wraps time.Duration for JSON compatibility.
type Duration struct {
	time.Duration
}

// DeepCopyInto copies metadata.
func (in *ObjectMeta) DeepCopyInto(out *ObjectMeta) {
	if in == nil || out == nil {
		return
	}
	*out = *in
	if in.Labels != nil {
		out.Labels = make(map[string]string, len(in.Labels))
		for k, v := range in.Labels {
			out.Labels[k] = v
		}
	}
}

// DeepCopyInto copies list metadata.
func (in *ListMeta) DeepCopyInto(out *ListMeta) {
	if in == nil || out == nil {
		return
	}
	*out = *in
}
