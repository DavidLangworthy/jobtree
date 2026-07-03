package keys

import "testing"

func TestNamespacedKey(t *testing.T) {
	cases := []struct {
		namespace string
		name      string
		want      string
	}{
		{"default", "train", "default/train"},
		{"team-a", "train", "team-a/train"},
		{"", "train", "default/train"},
	}
	for _, tc := range cases {
		if got := NamespacedKey(tc.namespace, tc.name); got != tc.want {
			t.Errorf("NamespacedKey(%q, %q) = %q, want %q", tc.namespace, tc.name, got, tc.want)
		}
	}
}
