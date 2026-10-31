package session

import (
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestDebugSessionRegistryCreateListDelete(t *testing.T) {
	registry := NewDebugSessionRegistry()

	created, err := registry.Create(contracts.CreateDebugSessionRequest{
		Namespace:   "default",
		ServiceName: "svc-a",
		ServicePort: 8080,
	})
	if err != nil {
		t.Fatalf("expected create to succeed, got %v", err)
	}
	if created.SessionID == "" {
		t.Fatalf("expected session id to be set")
	}
	if created.SessionToken == "" {
		t.Fatalf("expected session token to be set")
	}

	list := registry.List()
	if len(list) != 1 || list[0].SessionID != created.SessionID {
		t.Fatalf("unexpected list contents: %+v", list)
	}
	loaded, ok := registry.Get(created.SessionID)
	if !ok {
		t.Fatalf("expected get to return created session")
	}
	if loaded.SessionID != created.SessionID {
		t.Fatalf("expected matching session id, got %q", loaded.SessionID)
	}

	registry.Delete(created.SessionID)
	registry.Delete(created.SessionID)
	if _, ok := registry.Get(created.SessionID); ok {
		t.Fatalf("expected get to return false after delete")
	}
	if len(registry.List()) != 0 {
		t.Fatalf("expected empty registry after delete")
	}
}

func TestDebugSessionRegistryCreateValidationAndDefaults(t *testing.T) {
	registry := NewDebugSessionRegistry()

	if _, err := registry.Create(contracts.CreateDebugSessionRequest{
		ServicePort: 8080,
	}); err == nil {
		t.Fatalf("expected validation error for missing service_name")
	}

	if _, err := registry.Create(contracts.CreateDebugSessionRequest{
		ServiceName: "svc-a",
	}); err == nil {
		t.Fatalf("expected validation error for missing service_port")
	}

	created, err := registry.Create(contracts.CreateDebugSessionRequest{
		ServiceName: " svc-a ",
		ServicePort: 8080,
	})
	if err != nil {
		t.Fatalf("expected create to succeed, got %v", err)
	}
	if created.Namespace != "default" {
		t.Fatalf("expected default namespace, got %q", created.Namespace)
	}
	if created.ServiceName != "svc-a" {
		t.Fatalf("expected trimmed service name, got %q", created.ServiceName)
	}
}

func TestCreateSupersedesSessionForSameWorkload(t *testing.T) {
	registry := NewDebugSessionRegistry()

	first, err := registry.Create(contracts.CreateDebugSessionRequest{
		Namespace:   "shop",
		ServiceName: "orders-api",
		ServicePort: 8080,
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}

	second, err := registry.Create(contracts.CreateDebugSessionRequest{
		Namespace:   "shop",
		ServiceName: "orders-api",
		ServicePort: 8080,
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	if _, ok := registry.Get(first.SessionID); ok {
		t.Fatal("first session must be superseded by the second")
	}
	if _, ok := registry.Get(second.SessionID); !ok {
		t.Fatal("second session must be active")
	}
	if got := len(registry.List()); got != 1 {
		t.Fatalf("expected 1 session, got %d", got)
	}

	other, err := registry.Create(contracts.CreateDebugSessionRequest{
		Namespace:   "other-ns",
		ServiceName: "orders-api",
		ServicePort: 8080,
	})
	if err != nil {
		t.Fatalf("create other namespace: %v", err)
	}
	if _, ok := registry.Get(second.SessionID); !ok {
		t.Fatal("session in a different namespace must not be superseded")
	}
	if _, ok := registry.Get(other.SessionID); !ok {
		t.Fatal("other-namespace session must be active")
	}
}
