//go:build linux

package helper

import (
	"strings"
	"testing"
)

func TestBuildSystemdUnitUsesOwnerArgumentOnly(t *testing.T) {
	unit := buildSystemdUnit("/opt/krun-helper", "/home/testuser/.kube/config", "testuser")

	if strings.Contains(unit, "KRUN_HOME") {
		t.Fatalf("expected unit not to set KRUN_HOME:\n%s", unit)
	}
	if !strings.Contains(unit, "ExecStart=/opt/krun-helper --kubeconfig /home/testuser/.kube/config --owner testuser") {
		t.Fatalf("expected owner in ExecStart, got:\n%s", unit)
	}
}
