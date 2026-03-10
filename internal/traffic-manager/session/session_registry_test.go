package session

import (
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestDebugSessionRegistryCreateListDelete(t *testing.T) {
	registry := NewDebugSessionRegistry()

	created, err := registry.Create(contracts.CreateDebugSessionRequest{
		Namespace:   "default",
		ServiceName: "svc-a",
		ServicePort: 8080,
		LocalPort:   5000,
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
	if created.ClientID != "unknown" {
		t.Fatalf("expected default client id, got %q", created.ClientID)
	}
	if _, err := time.Parse(time.RFC3339, created.CreatedAt); err != nil {
		t.Fatalf("expected RFC3339 created_at, got %q", created.CreatedAt)
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

	if ok := registry.Delete(created.SessionID); !ok {
		t.Fatalf("expected delete to return true")
	}
	if ok := registry.Delete(created.SessionID); ok {
		t.Fatalf("expected second delete to return false")
	}
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
		LocalPort:   5000,
	}); err == nil {
		t.Fatalf("expected validation error for missing service_name")
	}

	if _, err := registry.Create(contracts.CreateDebugSessionRequest{
		ServiceName: "svc-a",
		LocalPort:   5000,
	}); err == nil {
		t.Fatalf("expected validation error for missing service_port")
	}

	if _, err := registry.Create(contracts.CreateDebugSessionRequest{
		ServiceName: "svc-a",
		ServicePort: 8080,
	}); err == nil {
		t.Fatalf("expected validation error for missing local_port")
	}

	created, err := registry.Create(contracts.CreateDebugSessionRequest{
		ServiceName: " svc-a ",
		ServicePort: 8080,
		LocalPort:   5000,
	})
	if err != nil {
		t.Fatalf("expected create to succeed, got %v", err)
	}
	if created.Namespace != "default" {
		t.Fatalf("expected default namespace, got %q", created.Namespace)
	}
	if created.Workload != "svc-a" {
		t.Fatalf("expected workload default to service name, got %q", created.Workload)
	}
	if created.ServiceName != "svc-a" {
		t.Fatalf("expected trimmed service name, got %q", created.ServiceName)
	}
}
