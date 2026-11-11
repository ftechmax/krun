package managerauth

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsureLocalTokenRemovesFailedWrite(t *testing.T) {
	const pathEnv = "KRUN_TEST_FAILED_TOKEN_PATH"
	if path := os.Getenv(pathEnv); path != "" {
		// Allow the 64 hex characters but reject the trailing newline. Without
		// cleanup, this failed write would leave a token ReadLocalToken accepts.
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: 64, Max: 64}); err != nil {
			t.Fatalf("limit token write size: %v", err)
		}
		if token, err := EnsureLocalToken(path); err == nil || token != "" {
			t.Fatalf("failed write returned token %q, error %v", token, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("failed token write left a file: %v", err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), dirName, tokenFileName)
	// Isolate the file-size limit so other tests can still write normally.
	cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureLocalTokenRemovesFailedWrite$")
	cmd.Env = append(os.Environ(), pathEnv+"="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed-write subprocess: %v\n%s", err, output)
	}
	if _, err := EnsureLocalToken(path); err != nil {
		t.Fatalf("retry token creation: %v", err)
	}
}
