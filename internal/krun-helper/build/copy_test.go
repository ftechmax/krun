package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSyncPlanCreatePhase(t *testing.T) {
	local := map[string]localFile{
		"new.txt": {relPath: "new.txt", localPath: "/tmp/new.txt", mtimeNs: 10, size: 5},
	}
	remote := map[string]remoteEntry{}

	plan := buildSyncPlan(local, remote)

	if len(plan.toUpload) != 1 || plan.toUpload[0].relPath != "new.txt" {
		t.Fatalf("expected new.txt in upload list, got %#v", plan.toUpload)
	}
	if len(plan.toDelete) != 0 {
		t.Fatalf("expected no delete entries, got %#v", plan.toDelete)
	}
}

func TestBuildSyncPlanChangedPhase(t *testing.T) {
	local := map[string]localFile{
		"same.txt":    {relPath: "same.txt", localPath: "/tmp/same.txt", mtimeNs: 10, size: 5},
		"changed.txt": {relPath: "changed.txt", localPath: "/tmp/changed.txt", mtimeNs: 20, size: 9},
	}
	remote := map[string]remoteEntry{
		"same.txt":    {mtimeNs: 10, size: 5},
		"changed.txt": {mtimeNs: 20, size: 8},
	}

	plan := buildSyncPlan(local, remote)

	if len(plan.toUpload) != 1 || plan.toUpload[0].relPath != "changed.txt" {
		t.Fatalf("expected changed.txt in upload list, got %#v", plan.toUpload)
	}
	if len(plan.toDelete) != 0 {
		t.Fatalf("expected no delete entries, got %#v", plan.toDelete)
	}
}

func TestBuildSyncPlanSecondPrecisionMtime(t *testing.T) {
	local := map[string]localFile{
		"same.txt": {relPath: "same.txt", localPath: "/tmp/same.txt", mtimeNs: 1700000000123456789, size: 5},
	}
	remote := map[string]remoteEntry{
		"same.txt": {mtimeNs: 1700000000000000000, size: 5},
	}

	plan := buildSyncPlan(local, remote)

	if len(plan.toUpload) != 0 {
		t.Fatalf("expected no upload entries, got %#v", plan.toUpload)
	}
	if len(plan.toDelete) != 0 {
		t.Fatalf("expected no delete entries, got %#v", plan.toDelete)
	}
}

func TestBuildSyncPlanRemoteNewerSkipsUpload(t *testing.T) {
	local := map[string]localFile{
		"same.txt": {relPath: "same.txt", localPath: "/tmp/same.txt", mtimeNs: 1700000000000000000, size: 5},
	}
	remote := map[string]remoteEntry{
		"same.txt": {mtimeNs: 1700000009000000000, size: 5},
	}

	plan := buildSyncPlan(local, remote)

	if len(plan.toUpload) != 0 {
		t.Fatalf("expected no upload entries when remote is newer, got %#v", plan.toUpload)
	}
}

func TestBuildSyncPlanDeletedPhase(t *testing.T) {
	local := map[string]localFile{
		"keep.txt": {relPath: "keep.txt", localPath: "/tmp/keep.txt", mtimeNs: 1, size: 1},
	}
	remote := map[string]remoteEntry{
		"keep.txt":   {mtimeNs: 1, size: 1},
		"stale1.txt": {mtimeNs: 2, size: 2},
		"stale2.txt": {mtimeNs: 3, size: 3},
	}

	plan := buildSyncPlan(local, remote)

	if len(plan.toDelete) != 2 {
		t.Fatalf("expected 2 delete entries, got %#v", plan.toDelete)
	}
	if plan.toDelete[0] != "stale1.txt" || plan.toDelete[1] != "stale2.txt" {
		t.Fatalf("unexpected delete order/content: %#v", plan.toDelete)
	}
}

func TestParseRemoteFindOutput(t *testing.T) {
	out := "a.txt\t1700000000.123456789\t11\ninvalid line\nb/c.txt\t1700000001.5\t22\n"
	parsed := parseRemoteFindOutput(out)

	if len(parsed) != 2 {
		t.Fatalf("expected 2 parsed entries, got %d", len(parsed))
	}
	if parsed["a.txt"].mtimeNs != 1700000000123456789 || parsed["a.txt"].size != 11 {
		t.Fatalf("unexpected parsed entry for a.txt: %#v", parsed["a.txt"])
	}
	if parsed["b/c.txt"].mtimeNs != 1700000001500000000 || parsed["b/c.txt"].size != 22 {
		t.Fatalf("unexpected parsed entry for b/c.txt: %#v", parsed["b/c.txt"])
	}
}

func TestCollectLocalFilesSkipWeb(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "service", "api.go"), "package main")
	writeTestFile(t, filepath.Join(root, "docs", "ignored.md"), "doc")
	writeTestFile(t, filepath.Join(root, "web", "ui.ts"), "ui")

	files, err := collectLocalFiles(root, true)
	if err != nil {
		t.Fatalf("collectLocalFiles returned error: %v", err)
	}

	if _, ok := files["service/api.go"]; !ok {
		t.Fatalf("expected service/api.go to be included")
	}
	if _, ok := files["docs/ignored.md"]; ok {
		t.Fatalf("docs directory should be excluded")
	}
	if _, ok := files["web/ui.ts"]; ok {
		t.Fatalf("web directory should be excluded when skipWeb=true")
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
