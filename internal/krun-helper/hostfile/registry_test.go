package hostfile

import (
	"slices"
	"testing"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestSessionHostsRegistryUpsertNormalizesAndDeduplicates(t *testing.T) {
	registry := NewSessionHostsRegistry()

	got := registry.Upsert("  session-a  ", []contracts.HostsEntry{
		{IP: " 127.0.0.1 ", Hostname: " svc.local "},
		{IP: "127.0.0.1", Hostname: "svc.local"},
		{IP: "", Hostname: "ignored.local"},
		{IP: "127.0.0.2", Hostname: ""},
	})

	want := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "svc.local"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected merged entries\nwant:\n%+v\ngot:\n%+v", want, got)
	}
}

func TestSessionHostsRegistryMergesAndScopedRemove(t *testing.T) {
	registry := NewSessionHostsRegistry()
	registry.Upsert("proj-a/svc-a", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "rabbitmq.local"},
		{IP: "127.0.0.1", Hostname: "mongo.local"},
	})
	got := registry.Upsert("proj-b/svc-b", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "rabbitmq.local"},
		{IP: "127.0.0.1", Hostname: "redis.local"},
	})

	wantMerged := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "mongo.local"},
		{IP: "127.0.0.1", Hostname: "rabbitmq.local"},
		{IP: "127.0.0.1", Hostname: "redis.local"},
	}
	if !slices.Equal(got, wantMerged) {
		t.Fatalf("unexpected merged entries\nwant:\n%+v\ngot:\n%+v", wantMerged, got)
	}

	got = registry.Remove("proj-a/svc-a")
	wantAfterRemove := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "rabbitmq.local"},
		{IP: "127.0.0.1", Hostname: "redis.local"},
	}
	if !slices.Equal(got, wantAfterRemove) {
		t.Fatalf("unexpected entries after scoped remove\nwant:\n%+v\ngot:\n%+v", wantAfterRemove, got)
	}
}

func TestSessionHostsRegistryClearOnEmptyRemoveAndClear(t *testing.T) {
	registry := NewSessionHostsRegistry()
	registry.Upsert("session-a", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "svc-a.local"},
	})

	got := registry.Remove("  ")
	if len(got) != 0 {
		t.Fatalf("expected empty result after global remove, got: %+v", got)
	}

	registry.Upsert("session-b", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "svc-b.local"},
	})
	registry.Clear()

	got = registry.Upsert("session-c", []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "svc-c.local"},
	})
	want := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "svc-c.local"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected entries after clear\nwant:\n%+v\ngot:\n%+v", want, got)
	}
}
