//go:build !unix

package cmd

// lockFile is a no-op where flock is unavailable. Atomic rename in Save
// still prevents torn reads; only concurrent read-modify-write cycles race.
func lockFile(path string) (func(), error) {
	return func() {}, nil
}
