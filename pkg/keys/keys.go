// Package keys defines the canonical namespaced-key form used to index runs,
// leases, and pods across the engine, controllers, and CLI.
package keys

// DefaultNamespace is assumed when an object or reference carries no namespace.
const DefaultNamespace = "default"

// NamespacedKey returns "<namespace>/<name>", treating an empty namespace as
// DefaultNamespace so references with and without an explicit namespace index
// the same object.
func NamespacedKey(namespace, name string) string {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	return namespace + "/" + name
}
