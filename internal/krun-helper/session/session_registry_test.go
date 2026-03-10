package session

import (
	"slices"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestDebugSessionRegistryUpsertListAndRemove(t *testing.T) {
	registry := NewDebugSessionRegistry()
	registry.Upsert("proj-b/svc-b", contracts.DebugServiceContext{ServiceName: "svc-b", InterceptPort: 5002})
	registry.Upsert("proj-a/svc-a", contracts.DebugServiceContext{ServiceName: "svc-a", InterceptPort: 5001})

	list := registry.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions, got %+v", list)
	}

	keys := []string{list[0].SessionKey, list[1].SessionKey}
	wantKeys := []string{"proj-a/svc-a", "proj-b/svc-b"}
	if !slices.Equal(keys, wantKeys) {
		t.Fatalf("unexpected key order: got=%v want=%v", keys, wantKeys)
	}

	registry.Remove("proj-a/svc-a")
	list = registry.List()
	if len(list) != 1 || list[0].SessionKey != "proj-b/svc-b" {
		t.Fatalf("unexpected sessions after remove: %+v", list)
	}

	registry.Clear()
	list = registry.List()
	if len(list) != 0 {
		t.Fatalf("expected empty sessions after clear, got %+v", list)
	}
}
