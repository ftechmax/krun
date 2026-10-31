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

	err := Write([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "rabbitmq.local"}})
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

	err := Write([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "new.local"}})
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

	if err := Write(entries); err != nil {
		t.Fatalf("first Update failed: %v", err)
	}
	first := mustRead(t, hostsPath)

	if err := Write(entries); err != nil {
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

	if err := Write(nil); err != nil {
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

	if err := Write([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "mq.local"}}); err != nil {
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

	if err := Write([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "new.local"}}); err != nil {
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

func TestHasBlock(t *testing.T) {
	dir := t.TempDir()

	clean := filepath.Join(dir, "clean")
	if err := os.WriteFile(clean, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	SetPathForTest(t, clean)
	if present, err := HasBlock(); err != nil || present {
		t.Fatalf("HasBlock on a clean file = (%v, %v), want (false, nil)", present, err)
	}

	withBlock := filepath.Join(dir, "withblock")
	if err := os.WriteFile(withBlock, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	SetPathForTest(t, withBlock)
	if err := Write([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "redis.default.svc"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if present, err := HasBlock(); err != nil || !present {
		t.Fatalf("HasBlock after a write = (%v, %v), want (true, nil)", present, err)
	}

	if err := Remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if present, err := HasBlock(); err != nil || present {
		t.Fatalf("HasBlock after remove = (%v, %v), want (false, nil)", present, err)
	}
}

func TestValidateRejectsUnsafeEntries(t *testing.T) {
	if err := Validate([]contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "redis.default.svc"}}); err != nil {
		t.Fatalf("a loopback entry must be accepted: %v", err)
	}
	if err := Validate([]contracts.HostsEntry{{IP: "::1", Hostname: "redis"}}); err != nil {
		t.Fatalf("IPv6 loopback must be accepted: %v", err)
	}

	for name, entry := range map[string]contracts.HostsEntry{
		"public address":  {IP: "93.184.216.34", Hostname: "example.com"},
		"private address": {IP: "10.0.0.5", Hostname: "internal"},
		"bad address":     {IP: "nonsense", Hostname: "redis"},
		"empty hostname":  {IP: "127.0.0.1", Hostname: "   "},
		"newline":         {IP: "127.0.0.1", Hostname: "a\n127.0.0.1 evil"},
		"space":           {IP: "127.0.0.1", Hostname: "a b"},
	} {
		if err := Validate([]contracts.HostsEntry{entry}); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}

	tooMany := make([]contracts.HostsEntry, maxEntries+1)
	for i := range tooMany {
		tooMany[i] = contracts.HostsEntry{IP: "127.0.0.1", Hostname: "svc"}
	}
	if err := Validate(tooMany); err == nil {
		t.Fatal("an oversized entry list must be refused")
	}
}
