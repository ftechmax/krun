package session

import "testing"

func TestManagerSessionRegistryUpsertGetRemoveClear(t *testing.T) {
	registry := NewManagerSessionRegistry()

	registry.Upsert("proj-a/svc-a", "sess-1")
	registry.Upsert("proj-b/svc-b", "sess-2")

	if got, ok := registry.Get("proj-a/svc-a"); !ok || got != "sess-1" {
		t.Fatalf("unexpected get result for proj-a/svc-a: %q %v", got, ok)
	}
	if got, ok := registry.Get("proj-b/svc-b"); !ok || got != "sess-2" {
		t.Fatalf("unexpected get result for proj-b/svc-b: %q %v", got, ok)
	}

	registry.Remove("proj-a/svc-a")
	if _, ok := registry.Get("proj-a/svc-a"); ok {
		t.Fatalf("expected proj-a/svc-a to be removed")
	}

	registry.Clear()
	if _, ok := registry.Get("proj-b/svc-b"); ok {
		t.Fatalf("expected registry to be empty after clear")
	}
}
