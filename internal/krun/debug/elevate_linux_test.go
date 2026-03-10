//go:build linux

package debug

import (
	"reflect"
	"testing"
)

func TestBuildLinuxLaunchSpecUsesDetachedBinaryForRoot(t *testing.T) {
	spec, err := buildLinuxLaunchSpec("/tmp/krun-helper", []string{"--kubeconfig", "/tmp/config"}, 0, "/usr/bin/sudo", "/usr/bin/pkexec")
	if err != nil {
		t.Fatalf("build launch spec: %v", err)
	}

	if spec.path != "/tmp/krun-helper" {
		t.Fatalf("expected helper binary path, got %q", spec.path)
	}
	if !spec.detached {
		t.Fatalf("expected detached launch for root")
	}

	wantArgs := []string{"--kubeconfig", "/tmp/config"}
	if !reflect.DeepEqual(spec.args, wantArgs) {
		t.Fatalf("unexpected args: want %v, got %v", wantArgs, spec.args)
	}
}

func TestBuildLinuxLaunchSpecPrefersSudo(t *testing.T) {
	spec, err := buildLinuxLaunchSpec("/tmp/krun-helper", []string{"--kubeconfig", "/tmp/config"}, 1000, "/usr/bin/sudo", "/usr/bin/pkexec")
	if err != nil {
		t.Fatalf("build launch spec: %v", err)
	}

	if spec.path != "/usr/bin/sudo" {
		t.Fatalf("expected sudo launcher, got %q", spec.path)
	}
	if spec.detached {
		t.Fatalf("expected sudo launch to stay attached to the prompting process")
	}

	wantArgs := []string{"-b", "--", "/tmp/krun-helper", "--kubeconfig", "/tmp/config"}
	if !reflect.DeepEqual(spec.args, wantArgs) {
		t.Fatalf("unexpected args: want %v, got %v", wantArgs, spec.args)
	}
}

func TestBuildLinuxLaunchSpecFallsBackToPkexec(t *testing.T) {
	spec, err := buildLinuxLaunchSpec("/tmp/krun-helper", []string{"--kubeconfig", "/tmp/config"}, 1000, "", "/usr/bin/pkexec")
	if err != nil {
		t.Fatalf("build launch spec: %v", err)
	}

	if spec.path != "/usr/bin/pkexec" {
		t.Fatalf("expected pkexec launcher, got %q", spec.path)
	}

	wantArgs := []string{"/bin/sh", "-c", pkexecDetachScript, "sh", "/tmp/krun-helper", "--kubeconfig", "/tmp/config"}
	if !reflect.DeepEqual(spec.args, wantArgs) {
		t.Fatalf("unexpected args: want %v, got %v", wantArgs, spec.args)
	}
}

func TestBuildLinuxLaunchSpecErrorsWithoutElevationTool(t *testing.T) {
	_, err := buildLinuxLaunchSpec("/tmp/krun-helper", nil, 1000, "", "")
	if err == nil {
		t.Fatalf("expected error without sudo or pkexec")
	}
}
