package session

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

func TestEnableAppliesHostsPortForwardsAndStream(t *testing.T) {
	env := newTestEnv(t)

	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("enable failed: %v", err)
	}

	wantHosts := []contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "rabbitmq.default.svc"}}
	if !slices.Equal(env.hosts, wantHosts) {
		t.Fatalf("unexpected desired hosts entries: %+v", env.hosts)
	}

	if env.forwards.upsertCalls != 1 {
		t.Fatalf("expected one port-forward upsert, got %d", env.forwards.upsertCalls)
	}
	if env.forwards.lastKey != "awesome-app3/awesome-app3-worker" {
		t.Fatalf("unexpected session key %q", env.forwards.lastKey)
	}
	wantForwards := []contracts.PortForward{
		{Namespace: "default", Service: "rabbitmq", LocalPort: 5672, RemotePort: 5672},
	}
	if !slices.Equal(env.forwards.lastForwards, wantForwards) {
		t.Fatalf("unexpected port-forwards: %+v", env.forwards.lastForwards)
	}

	if env.api.createCalls != 1 {
		t.Fatalf("expected one manager create, got %d", env.api.createCalls)
	}
	if env.lastAttach.interceptPort != 5001 {
		t.Fatalf("expected stream attached to intercept port 5001, got %d", env.lastAttach.interceptPort)
	}
	if env.lastAttach.sessionID != "mgr-session-1" {
		t.Fatalf("expected stream attached to mgr-session-1, got %q", env.lastAttach.sessionID)
	}

	sessions := env.manager.List()
	if len(sessions) != 1 || sessions[0].SessionKey != "awesome-app3/awesome-app3-worker" {
		t.Fatalf("unexpected session list: %+v", sessions)
	}
}

func TestEnableRollsBackWhenManagerCreateFails(t *testing.T) {
	env := newTestEnv(t)
	env.api.createErr = errors.New("manager unavailable")

	if err := env.manager.Enable(workerContext()); err == nil {
		t.Fatal("expected enable to fail when the manager rejects the session")
	}

	if env.forwards.removeCalls != 1 {
		t.Fatalf("expected the port-forwards to be rolled back, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.hosts) != 0 {
		t.Fatalf("a rolled-back session must leave no desired hosts, got %+v", env.hosts)
	}
	if len(env.manager.List()) != 0 {
		t.Fatalf("expected no registered session after rollback, got %+v", env.manager.List())
	}
}

func TestEnableRollsBackWhenPortForwardUpdateFails(t *testing.T) {
	env := newTestEnv(t)
	env.forwards.upsertErr = errors.New("forward unavailable")

	if err := env.manager.Enable(workerContext()); err == nil {
		t.Fatal("expected enable to fail")
	}
	if env.api.createCalls != 0 {
		t.Fatal("manager session must not be created after the forward update fails")
	}
	if len(env.hosts) != 0 {
		t.Fatalf("a failed enable must leave no desired hosts, got %+v", env.hosts)
	}
}

func TestEnableRollsBackManagerSessionWhenStreamAttachFails(t *testing.T) {
	env := newTestEnv(t)
	env.attachErr = errors.New("stream refused")

	if err := env.manager.Enable(workerContext()); err == nil {
		t.Fatal("expected enable to fail when the stream cannot attach")
	}

	// A manager session with no stream would leave an injected sidecar
	// intercepting traffic that nothing is there to serve.
	if env.api.deleteCalls != 1 {
		t.Fatalf("expected the manager session to be rolled back, got %d delete calls", env.api.deleteCalls)
	}
	if env.api.lastDeletedID != "mgr-session-1" {
		t.Fatalf("unexpected rolled back manager session %q", env.api.lastDeletedID)
	}
	if env.forwards.removeCalls != 1 {
		t.Fatalf("expected the port-forwards to be rolled back, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.manager.List()) != 0 {
		t.Fatalf("expected no registered session after rollback, got %+v", env.manager.List())
	}
}

func TestEnableReplacesPreviousSession(t *testing.T) {
	env := newTestEnv(t)
	env.api.createIDs = []string{"mgr-session-1", "mgr-session-2"}

	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}
	firstStream := env.lastAttach

	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("second enable failed: %v", err)
	}

	if env.api.createCalls != 2 {
		t.Fatalf("expected 2 manager creates, got %d", env.api.createCalls)
	}
	if env.api.deleteCalls != 1 || env.api.lastDeletedID != "mgr-session-1" {
		t.Fatalf("expected the previous manager session to be deleted, got %d deletes of %q", env.api.deleteCalls, env.api.lastDeletedID)
	}
	if firstStream.closes != 1 {
		t.Fatalf("expected the previous stream to be closed once, got %d", firstStream.closes)
	}
	if env.forwards.removeCalls != 0 {
		t.Fatalf("successful replacement must preserve forwards, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.manager.List()) != 1 {
		t.Fatalf("expected exactly one session after re-enable, got %+v", env.manager.List())
	}
}

func TestEnableReplacementUpdatesDependenciesWithoutPreemptiveRemove(t *testing.T) {
	env := newTestEnv(t)
	env.api.createIDs = []string{"mgr-session-1", "mgr-session-2"}

	first := dependencyContext("proj", "service", "rabbitmq.default.svc", 5672, 5001)
	second := dependencyContext("proj", "service", "redis.default.svc", 6379, 5001)
	if err := env.manager.Enable(first); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}
	if err := env.manager.Enable(second); err != nil {
		t.Fatalf("replacement failed: %v", err)
	}

	if env.forwards.removeCalls != 0 {
		t.Fatalf("replacement called Remove %d times", env.forwards.removeCalls)
	}
	wantForwards := []contracts.PortForward{{Namespace: "default", Service: "redis", LocalPort: 6379, RemotePort: 6379}}
	if !slices.Equal(env.forwards.lastForwards, wantForwards) {
		t.Fatalf("replacement forwards = %+v, want %+v", env.forwards.lastForwards, wantForwards)
	}
	wantHosts := []contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "redis.default.svc"}}
	if !slices.Equal(env.hosts, wantHosts) {
		t.Fatalf("replacement desired hosts = %+v, want %+v", env.hosts, wantHosts)
	}
}

func TestEnableReplacementUpsertFailureCleansPreservedLocalState(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}

	env.forwards.upsertErr = errors.New("replacement forward failed")
	err := env.manager.Enable(workerContext())
	if err == nil || !strings.Contains(err.Error(), "replacement forward failed") {
		t.Fatalf("expected replacement forward error, got %v", err)
	}
	if env.forwards.removeCalls != 1 {
		t.Fatalf("preserved forwards were not cleaned, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.hosts) != 0 {
		t.Fatalf("replacement rollback left desired hosts: %+v", env.hosts)
	}
	if sessions := env.manager.List(); len(sessions) != 0 {
		t.Fatalf("clean rollback left a tracked session: %+v", sessions)
	}
}

func TestEnablePreservesPreviousSessionWhenReplacementDeleteFails(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}
	firstStream := env.lastAttach

	env.api.deleteErr = errors.New("manager unavailable")
	err := env.manager.Enable(workerContext())
	if err == nil || !strings.Contains(err.Error(), "replace previous manager session") {
		t.Fatalf("expected replacement delete error, got %v", err)
	}
	if firstStream.closes != 0 {
		t.Fatal("the previous stream must stay active when its manager session cannot be deleted")
	}
	if env.api.createCalls != 1 {
		t.Fatalf("replacement session must not be created, got %d creates", env.api.createCalls)
	}
	sessions := env.manager.List()
	if len(sessions) != 1 || sessions[0].State != contracts.HelperSessionStateActive {
		t.Fatalf("previous active session was not preserved: %+v", sessions)
	}
}

func TestEnableTracksIncompleteRollbackForDisableRetry(t *testing.T) {
	env := newTestEnv(t)
	env.attachErr = errors.New("stream attach failed")
	env.api.deleteErr = errors.New("manager delete failed")

	err := env.manager.Enable(workerContext())
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("expected rollback failure, got %v", err)
	}
	sessions := env.manager.List()
	if len(sessions) != 1 || sessions[0].State != contracts.HelperSessionStateCleanupPending {
		t.Fatalf("expected cleanup-pending session, got %+v", sessions)
	}

	env.api.deleteErr = nil
	if _, err := env.manager.Disable(workerContext()); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	if len(env.manager.List()) != 0 {
		t.Fatalf("cleanup retry left a session: %+v", env.manager.List())
	}
}

func TestDisableRemovesOnlyItsOwnState(t *testing.T) {
	env := newTestEnv(t)
	env.api.createIDs = []string{"mgr-a", "mgr-b"}

	if err := env.manager.Enable(dependencyContext("proj-a", "svc-a", "rabbitmq.default.svc", 5672, 5001)); err != nil {
		t.Fatalf("enable svc-a failed: %v", err)
	}
	if err := env.manager.Enable(dependencyContext("proj-b", "svc-b", "redis.default.svc", 6379, 5002)); err != nil {
		t.Fatalf("enable svc-b failed: %v", err)
	}
	streamA := env.streams[0]

	found, err := env.manager.Disable(dependencyContext("proj-a", "svc-a", "rabbitmq.default.svc", 5672, 5001))
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if !found {
		t.Fatal("expected disable to report the session as found")
	}

	// The hosts file holds one block for every session, so disabling one must
	// leave the other's entries in place.
	wantHosts := []contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "redis.default.svc"}}
	if !slices.Equal(env.hosts, wantHosts) {
		t.Fatalf("unexpected desired hosts after disable: %+v", env.hosts)
	}
	if env.forwards.lastKey != "proj-a/svc-a" {
		t.Fatalf("expected port-forwards removed for proj-a/svc-a, got %q", env.forwards.lastKey)
	}
	if env.api.lastDeletedID != "mgr-a" {
		t.Fatalf("expected manager session mgr-a to be deleted, got %q", env.api.lastDeletedID)
	}
	if streamA.closes != 1 {
		t.Fatalf("expected the disabled session's stream to be closed, got %d", streamA.closes)
	}

	sessions := env.manager.List()
	if len(sessions) != 1 || sessions[0].SessionKey != "proj-b/svc-b" {
		t.Fatalf("expected only proj-b/svc-b to remain, got %+v", sessions)
	}
}

func TestDisableReportsUnknownSession(t *testing.T) {
	env := newTestEnv(t)

	found, err := env.manager.Disable(workerContext())
	if err != nil {
		t.Fatalf("disable of an unknown session returned an error: %v", err)
	}
	if found {
		t.Fatal("expected disable to report no session was found")
	}
}

func TestDisableKeepsSessionWhenTeardownFails(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("enable failed: %v", err)
	}

	env.api.deleteErr = errors.New("manager unreachable")
	found, err := env.manager.Disable(workerContext())
	if !found || err == nil {
		t.Fatalf("expected a found session and a teardown error, got found=%v err=%v", found, err)
	}

	// The session stays so the user can retry once the manager is reachable;
	// dropping it would strand the injected sidecar in the cluster.
	if len(env.manager.List()) != 1 {
		t.Fatalf("expected the session to survive a failed teardown, got %+v", env.manager.List())
	}
	if env.manager.List()[0].State != contracts.HelperSessionStateCleanupPending {
		t.Fatalf("expected cleanup-pending state, got %+v", env.manager.List())
	}

	env.api.deleteErr = nil
	if _, err := env.manager.Disable(workerContext()); err != nil {
		t.Fatalf("retried disable failed: %v", err)
	}
	if len(env.manager.List()) != 0 {
		t.Fatalf("expected the retry to remove the session, got %+v", env.manager.List())
	}
}

func TestCloseReleasesLocalStateAndLeavesManagerSessions(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable(workerContext()); err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	stream := env.lastAttach

	env.manager.Close()

	if stream.closes != 1 {
		t.Fatalf("expected the stream to be closed, got %d", stream.closes)
	}
	if env.forwards.clearCalls != 1 {
		t.Fatalf("expected the port-forwards to be cleared, got %d", env.forwards.clearCalls)
	}
	if len(env.hosts) != 0 {
		t.Fatalf("Close must leave no desired hosts, got %+v", env.hosts)
	}
	// Helper shutdown only owns local cleanup; manager-side cleanup happens on
	// replacement or when the manager next starts.
	if env.api.deleteCalls != 0 {
		t.Fatalf("expected no manager session deletes on shutdown, got %d", env.api.deleteCalls)
	}
}

func TestListReturnsWhileEnableIsBlocked(t *testing.T) {
	env := newTestEnv(t)
	env.attachStarted = make(chan struct{})
	env.attachRelease = make(chan struct{})
	enableDone := make(chan error, 1)
	go func() { enableDone <- env.manager.Enable(workerContext()) }()

	<-env.attachStarted
	listDone := make(chan []contracts.HelperDebugSession, 1)
	go func() { listDone <- env.manager.List() }()
	select {
	case sessions := <-listDone:
		if len(sessions) != 0 {
			t.Fatalf("in-flight uncommitted enable was listed: %+v", sessions)
		}
	case <-time.After(time.Second):
		t.Fatal("List blocked behind Enable")
	}

	close(env.attachRelease)
	if err := <-enableDone; err != nil {
		t.Fatalf("enable failed: %v", err)
	}
}

func TestCloseWaitsForEnableThenCleansItsResources(t *testing.T) {
	env := newTestEnv(t)
	env.attachStarted = make(chan struct{})
	env.attachRelease = make(chan struct{})
	enableDone := make(chan error, 1)
	go func() { enableDone <- env.manager.Enable(workerContext()) }()
	<-env.attachStarted

	closeDone := make(chan struct{})
	go func() {
		env.manager.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned before Enable completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(env.attachRelease)
	if err := <-enableDone; err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	<-closeDone
	if env.lastAttach.closes != 1 || env.forwards.clearCalls != 1 || len(env.hosts) != 0 {
		t.Fatalf("Close did not clean completed Enable: stream=%d clear=%d desiredHosts=%+v",
			env.lastAttach.closes, env.forwards.clearCalls, env.hosts)
	}
}

func TestMergeHostEntriesIncludesAliasesAndDeduplicates(t *testing.T) {
	entries := mergeHostEntries(map[string]*Session{
		"a": {Context: config.Service{ServiceDependencies: []config.ServiceDependency{
			{Host: "mongo.default.svc", Port: 27017, Aliases: []string{"mongodb-server-0.mongo.default.svc.cluster.local"}},
		}}},
		"b": {Context: config.Service{ServiceDependencies: []config.ServiceDependency{
			{Host: "mongo.default.svc", Port: 27017},
			{Host: "", Port: 1},
		}}},
	})

	want := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "mongo.default.svc"},
		{IP: "127.0.0.1", Hostname: "mongodb-server-0.mongo.default.svc.cluster.local"},
	}
	if !slices.Equal(entries, want) {
		t.Fatalf("unexpected merged hosts entries:\n got=%+v\nwant=%+v", entries, want)
	}
}

func workerContext() config.Service {
	return dependencyContext("awesome-app3", "awesome-app3-worker", "rabbitmq.default.svc", 5672, 5001)
}

func dependencyContext(project, service, host string, dependencyPort, interceptPort int) config.Service {
	return config.Service{
		Project:       project,
		Name:          service,
		ContainerPort: 8080,
		InterceptPort: interceptPort,
		ServiceDependencies: []config.ServiceDependency{
			{Host: host, Service: strings.Split(host, ".")[0], Namespace: "default", Port: dependencyPort},
		},
	}
}

type testEnv struct {
	hosts    []contracts.HostsEntry
	hostsErr error
	manager  *Manager
	forwards *fakeForwards
	api      *fakeAPI

	attachErr     error
	attachStarted chan struct{}
	attachRelease chan struct{}
	streams       []*fakeStream
	lastAttach    *fakeStream
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	env := &testEnv{
		forwards: &fakeForwards{},
		api:      &fakeAPI{},
	}
	env.manager = NewManager(Options{
		Forwards:   env.forwards,
		WriteHosts: func(entries []contracts.HostsEntry) error { env.hosts = entries; return env.hostsErr },
		API:        env.api,
		Attach: func(sessionID string, sessionToken string, interceptPort int) (Stream, error) {
			if env.attachStarted != nil {
				close(env.attachStarted)
				<-env.attachRelease
			}
			if env.attachErr != nil {
				return nil, env.attachErr
			}
			stream := &fakeStream{sessionID: sessionID, sessionToken: sessionToken, interceptPort: interceptPort}
			env.streams = append(env.streams, stream)
			env.lastAttach = stream
			return stream, nil
		},
	})
	return env
}

type fakeForwards struct {
	upsertCalls  int
	removeCalls  int
	clearCalls   int
	lastKey      string
	lastForwards []contracts.PortForward
	upsertErr    error
}

func (f *fakeForwards) Upsert(sessionKey string, forwards []contracts.PortForward) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upsertCalls++
	f.lastKey = sessionKey
	f.lastForwards = append([]contracts.PortForward(nil), forwards...)
	return nil
}

func (f *fakeForwards) Remove(sessionKey string) {
	f.removeCalls++
	f.lastKey = sessionKey
}

func (f *fakeForwards) Clear() {
	f.clearCalls++
}

type fakeAPI struct {
	createCalls   int
	deleteCalls   int
	createIDs     []string
	lastDeletedID string
	createErr     error
	deleteErr     error
}

func (f *fakeAPI) CreateSession(serviceContext config.Service) (contracts.DebugSession, error) {
	f.createCalls++
	if f.createErr != nil {
		return contracts.DebugSession{}, f.createErr
	}

	sessionID := "mgr-session-1"
	if len(f.createIDs) > 0 {
		sessionID = f.createIDs[0]
		f.createIDs = f.createIDs[1:]
	}
	return contracts.DebugSession{
		SessionID:    sessionID,
		SessionToken: sessionID + "-token",
		ServiceName:  serviceContext.Name,
	}, nil
}

func (f *fakeAPI) DeleteSession(sessionID string) error {
	f.deleteCalls++
	f.lastDeletedID = sessionID
	return f.deleteErr
}

type fakeStream struct {
	sessionID     string
	sessionToken  string
	interceptPort int
	closes        int
}

func (f *fakeStream) Close() { f.closes++ }

func TestHostsFailureKeepsActiveSessionForRetry(t *testing.T) {
	env := newTestEnv(t)
	env.hostsErr = errors.New("writer unavailable")
	if err := env.manager.Enable(workerContext()); err == nil || !strings.Contains(err.Error(), "hosts update failed") {
		t.Fatalf("expected hosts error, got %v", err)
	}
	if sessions := env.manager.List(); len(sessions) != 1 || sessions[0].State != contracts.HelperSessionStateActive {
		t.Fatalf("hosts failure lost active session: %+v", sessions)
	}
	env.hostsErr = nil
	if _, err := env.manager.Disable(workerContext()); err != nil {
		t.Fatal(err)
	}
	if len(env.hosts) != 0 {
		t.Fatalf("disable left hosts: %+v", env.hosts)
	}
}

func TestHostsWritesAreSerializedWithSessionChanges(t *testing.T) {
	env := newTestEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	writes := make(chan []contracts.HostsEntry, 3)
	env.manager.writeHosts = func(entries []contracts.HostsEntry) error {
		writes <- entries
		if len(entries) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- env.manager.Enable(workerContext()) }()
	<-started
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- env.manager.Enable(dependencyContext("other", "api", "redis.default.svc", 6379, 5002))
	}()
	select {
	case <-secondDone:
		t.Fatal("second enable passed an in-flight hosts update")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if first, second := <-writes, <-writes; len(first) != 1 || len(second) != 2 {
		t.Fatalf("hosts writes out of order: %+v, %+v", first, second)
	}
}
