package hostfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestUpdateCreatesBlock(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "127.0.0.1 localhost\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	err := Update([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "rabbitmq.local"}})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	want := "127.0.0.1 localhost\n" +
		"##### KRUN #####\n" +
		"127.0.0.1\trabbitmq.local\n" +
		"##### END KRUN #####\n"
	if got != want {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestUpdateReplacesBlockAndPreservesContent(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "line1\n##### KRUN #####\n10.0.0.1\told\n##### END KRUN #####\nline2\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	err := Update([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "new.local"}})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	want := "line1\nline2\n" +
		"##### KRUN #####\n" +
		"127.0.0.1\tnew.local\n" +
		"##### END KRUN #####\n"
	if got != want {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRemoveDeletesBlock(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "line1\n##### KRUN #####\n127.0.0.1\tapp.local\n##### END KRUN #####\nline2\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	if err := Remove(); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	want := "line1\nline2\n"
	if got != want {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestRemoveNoopWhenBlockAbsent(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "127.0.0.1 localhost\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	if err := Remove(); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	if got != initial {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", initial, got)
	}
}

func TestUpdateIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "127.0.0.1 localhost\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)
	entries := []contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "db.local"}}

	if err := Update(entries); err != nil {
		t.Fatalf("first Update failed: %v", err)
	}
	first := mustRead(t, hostsPath)

	if err := Update(entries); err != nil {
		t.Fatalf("second Update failed: %v", err)
	}
	second := mustRead(t, hostsPath)

	if first != second {
		t.Fatalf("expected idempotent update\nfirst:\n%q\nsecond:\n%q", first, second)
	}
}

func TestUpdateEmptyEntriesRemovesBlock(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "line1\n##### KRUN #####\n127.0.0.1\tapp.local\n##### END KRUN #####\nline2\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	if err := Update(nil); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	want := "line1\nline2\n"
	if got != want {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestUpdatePreservesCRLF(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "127.0.0.1 localhost\r\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	if err := Update([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "mq.local"}}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	want := "127.0.0.1 localhost\r\n" +
		"##### KRUN #####\r\n" +
		"127.0.0.1\tmq.local\r\n" +
		"##### END KRUN #####\r\n"
	if got != want {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestUpdateMalformedExistingBlock(t *testing.T) {
	tmpDir := t.TempDir()
	hostsPath := filepath.Join(tmpDir, "hosts")
	initial := "line1\n##### KRUN #####\n127.0.0.1\told.local\n"
	if err := os.WriteFile(hostsPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial hosts: %v", err)
	}

	overrideHostsPath(t, hostsPath)

	if err := Update([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "new.local"}}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got := mustRead(t, hostsPath)
	want := "line1\n" +
		"##### KRUN #####\n" +
		"127.0.0.1\tnew.local\n" +
		"##### END KRUN #####\n"
	if got != want {
		t.Fatalf("unexpected hosts file\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func overrideHostsPath(t *testing.T, hostsPath string) {
	t.Helper()
	original := hostsFilePathResolver
	hostsFilePathResolver = func() (string, error) {
		return hostsPath, nil
	}
	t.Cleanup(func() {
		hostsFilePathResolver = original
	})
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
