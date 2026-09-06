//go:build linux

package debug

import "testing"

func TestElevationCommandAsRootRunsDirectly(t *testing.T) {
	name, args, err := elevationCommand("/usr/local/bin/krun-helper", []string{"apply-hosts"}, 0)
	if err != nil {
		t.Fatalf("root case: %v", err)
	}
	if name != "/usr/local/bin/krun-helper" {
		t.Fatalf("root must exec the binary directly, got %q", name)
	}
	if len(args) != 1 || args[0] != "apply-hosts" {
		t.Fatalf("unexpected args %+v", args)
	}
}

func TestElevationCommandUsesSudoWithArgumentTerminator(t *testing.T) {
	name, args, err := elevationCommand("/usr/local/bin/krun-helper", []string{"apply-hosts"}, 1000)
	if err != nil {
		t.Skip("sudo is not installed in this environment")
	}
	if name == "/usr/local/bin/krun-helper" {
		t.Fatal("a non-root euid must elevate rather than exec directly")
	}
	// "--" keeps a binary path that starts with a dash from being read as a
	// sudo option.
	want := []string{"--", "/usr/local/bin/krun-helper", "apply-hosts"}
	if len(args) != len(want) {
		t.Fatalf("args = %+v, want %+v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args = %+v, want %+v", args, want)
		}
	}
}
