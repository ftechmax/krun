//go:build linux

package debug

import (
	"slices"
	"testing"
)

func TestBuildLinuxLaunchSpecAsRootSkipsElevation(t *testing.T) {
	spec, err := buildLinuxLaunchSpec("/usr/local/bin/krun-helper", []string{"hosts-daemon"}, 0, "/usr/bin/sudo", "/usr/bin/pkexec")
	if err != nil {
		t.Fatalf("root case: %v", err)
	}
	if spec.path != "/usr/local/bin/krun-helper" || !spec.detached {
		t.Fatalf("root must run the binary directly and detach, got %+v", spec)
	}
	if !slices.Equal(spec.args, []string{"hosts-daemon"}) {
		t.Fatalf("unexpected args %+v", spec.args)
	}
}

func TestBuildLinuxLaunchSpecPrefersSudo(t *testing.T) {
	spec, err := buildLinuxLaunchSpec("/usr/local/bin/krun-helper", []string{"hosts-daemon"}, 1000, "/usr/bin/sudo", "/usr/bin/pkexec")
	if err != nil {
		t.Fatalf("sudo case: %v", err)
	}
	if spec.path != "/usr/bin/sudo" {
		t.Fatalf("expected sudo, got %q", spec.path)
	}
	// sudo runs in the foreground for its password prompt; the shell script
	// detaches the daemon from the CLI terminal.
	want := []string{"--", "/bin/sh", "-c", pkexecDetachScript, "sh", "/usr/local/bin/krun-helper", "hosts-daemon"}
	if !slices.Equal(spec.args, want) {
		t.Fatalf("args = %+v, want %+v", spec.args, want)
	}
	if spec.detached {
		t.Fatal("sudo must stay in the foreground so it can prompt for a password")
	}
}

func TestBuildLinuxLaunchSpecFallsBackToPkexec(t *testing.T) {
	spec, err := buildLinuxLaunchSpec("/usr/local/bin/krun-helper", []string{"hosts-daemon"}, 1000, "", "/usr/bin/pkexec")
	if err != nil {
		t.Fatalf("pkexec case: %v", err)
	}
	if spec.path != "/usr/bin/pkexec" {
		t.Fatalf("expected pkexec, got %q", spec.path)
	}
	if !slices.Contains(spec.args, "/usr/local/bin/krun-helper") {
		t.Fatalf("pkexec args must carry the binary, got %+v", spec.args)
	}
}

func TestBuildLinuxLaunchSpecWithoutElevationFails(t *testing.T) {
	if _, err := buildLinuxLaunchSpec("/usr/local/bin/krun-helper", nil, 1000, "", ""); err == nil {
		t.Fatal("expected an error when neither sudo nor pkexec is available")
	}
}
