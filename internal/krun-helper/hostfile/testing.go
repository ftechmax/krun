package hostfile

import "testing"

// SetPathForTest points the package at a temporary hosts file for the duration
// of one test. It exists so packages outside this one — notably the
// krun-helper apply-hosts command — can exercise real writes without touching
// the machine's /etc/hosts.
func SetPathForTest(t *testing.T, path string) {
	t.Helper()
	original := hostsFilePathResolver
	hostsFilePathResolver = func() (string, error) { return path, nil }
	t.Cleanup(func() { hostsFilePathResolver = original })
}
