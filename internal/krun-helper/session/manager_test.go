package session

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
)

func TestEnableAppliesHostsPortForwardsAndStream(t *testing.T) {
	env := newTestEnv(t)

	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("enable failed: %v", err)
	}

	wantHosts := []contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "rabbitmq.default.svc"}}
	if !slices.Equal(env.hosts.lastWrite, wantHosts) {
		t.Fatalf("unexpected hosts entries: %+v", env.hosts.lastWrite)
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

	if err := env.manager.Enable("", workerContext()); err == nil {
		t.Fatal("expected enable to fail when the manager rejects the session")
	}

	if env.forwards.removeCalls != 1 {
		t.Fatalf("expected the port-forwards to be rolled back, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.hosts.lastWrite) != 0 {
		t.Fatalf("expected the hosts entries to be rolled back, got %+v", env.hosts.lastWrite)
	}
	if len(env.manager.List()) != 0 {
		t.Fatalf("expected no registered session after rollback, got %+v", env.manager.List())
	}
}

func TestEnableStopsWhenHostsUpdateFails(t *testing.T) {
	env := newTestEnv(t)
	env.hosts.writeErr = errors.New("hosts unavailable")

	if err := env.manager.Enable("", workerContext()); err == nil {
		t.Fatal("expected enable to fail")
	}
	if env.forwards.upsertCalls != 0 || env.api.createCalls != 0 {
		t.Fatal("later enable steps must not run after the hosts update fails")
	}
	if len(env.manager.List()) != 0 {
		t.Fatal("a clean failure must not register a session")
	}
}

func TestEnableRollsBackWhenPortForwardUpdateFails(t *testing.T) {
	env := newTestEnv(t)
	env.forwards.upsertErr = errors.New("forward unavailable")

	if err := env.manager.Enable("", workerContext()); err == nil {
		t.Fatal("expected enable to fail")
	}
	if env.api.createCalls != 0 {
		t.Fatal("manager session must not be created after the forward update fails")
	}
	if len(env.hosts.lastWrite) != 0 {
		t.Fatalf("hosts must be restored, got %+v", env.hosts.lastWrite)
	}
}

func TestEnableRollsBackManagerSessionWhenStreamAttachFails(t *testing.T) {
	env := newTestEnv(t)
	env.attachErr = errors.New("stream refused")

	if err := env.manager.Enable("", workerContext()); err == nil {
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

	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}
	firstStream := env.lastAttach

	if err := env.manager.Enable("", workerContext()); err != nil {
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
	if err := env.manager.Enable("", first); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}
	if err := env.manager.Enable("", second); err != nil {
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
	if !slices.Equal(env.hosts.lastWrite, wantHosts) {
		t.Fatalf("replacement hosts = %+v, want %+v", env.hosts.lastWrite, wantHosts)
	}
}

func TestEnableReplacementHostsFailureCleansPreservedLocalState(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}

	env.hosts.writeErrors = []error{errors.New("replacement hosts failed"), nil}
	err := env.manager.Enable("", workerContext())
	if err == nil || !strings.Contains(err.Error(), "replacement hosts failed") {
		t.Fatalf("expected replacement hosts error, got %v", err)
	}
	if env.forwards.removeCalls != 1 {
		t.Fatalf("preserved forwards were not cleaned, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.hosts.lastWrite) != 0 {
		t.Fatalf("preserved hosts were not cleaned: %+v", env.hosts.lastWrite)
	}
	if sessions := env.manager.List(); len(sessions) != 0 {
		t.Fatalf("clean rollback left a tracked session: %+v", sessions)
	}
}

func TestEnableReplacementUpsertFailureCleansPreservedLocalState(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}

	env.forwards.upsertErr = errors.New("replacement forward failed")
	err := env.manager.Enable("", workerContext())
	if err == nil || !strings.Contains(err.Error(), "replacement forward failed") {
		t.Fatalf("expected replacement forward error, got %v", err)
	}
	if env.forwards.removeCalls != 1 {
		t.Fatalf("preserved forwards were not cleaned, got %d remove calls", env.forwards.removeCalls)
	}
	if len(env.hosts.lastWrite) != 0 {
		t.Fatalf("replacement hosts were not cleaned: %+v", env.hosts.lastWrite)
	}
	if sessions := env.manager.List(); len(sessions) != 0 {
		t.Fatalf("clean rollback left a tracked session: %+v", sessions)
	}
}

func TestEnableReplacementCleanupFailureIsRetryable(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}

	env.hosts.writeErrors = []error{errors.New("replacement hosts failed"), nil}
	env.forwards.removeErr = errors.New("cleanup forward failed")
	err := env.manager.Enable("", workerContext())
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("expected replacement rollback failure, got %v", err)
	}
	sessions := env.manager.List()
	if len(sessions) != 1 || sessions[0].State != contracts.HelperSessionStateCleanupPending {
		t.Fatalf("expected cleanup-pending replacement, got %+v", sessions)
	}

	env.forwards.removeErr = nil
	if _, err := env.manager.Disable("", workerContext()); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
}

func TestEnablePreservesPreviousSessionWhenReplacementDeleteFails(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("first enable failed: %v", err)
	}
	firstStream := env.lastAttach

	env.api.deleteErr = errors.New("manager unavailable")
	err := env.manager.Enable("", workerContext())
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
	tests := []struct {
		name       string
		configure  func(*testEnv)
		makeUsable func(*testEnv)
	}{
		{
			name: "manager session delete",
			configure: func(env *testEnv) {
				env.api.deleteErr = errors.New("manager delete failed")
			},
			makeUsable: func(env *testEnv) { env.api.deleteErr = nil },
		},
		{
			name: "port-forward remove",
			configure: func(env *testEnv) {
				env.forwards.removeErr = errors.New("forward remove failed")
			},
			makeUsable: func(env *testEnv) { env.forwards.removeErr = nil },
		},
		{
			name: "hosts restore",
			configure: func(env *testEnv) {
				env.hosts.writeErrors = []error{nil, errors.New("hosts restore failed")}
			},
			makeUsable: func(*testEnv) {},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.attachErr = errors.New("stream attach failed")
			testCase.configure(env)

			err := env.manager.Enable("", workerContext())
			if err == nil || !strings.Contains(err.Error(), "rollback failed") {
				t.Fatalf("expected rollback failure, got %v", err)
			}
			sessions := env.manager.List()
			if len(sessions) != 1 || sessions[0].State != contracts.HelperSessionStateCleanupPending {
				t.Fatalf("expected cleanup-pending session, got %+v", sessions)
			}

			testCase.makeUsable(env)
			if _, err := env.manager.Disable("", workerContext()); err != nil {
				t.Fatalf("cleanup retry failed: %v", err)
			}
			if len(env.manager.List()) != 0 {
				t.Fatalf("cleanup retry left a session: %+v", env.manager.List())
			}
		})
	}
}

func TestEnableReportsEveryRollbackFailure(t *testing.T) {
	env := newTestEnv(t)
	env.attachErr = errors.New("attach failed")
	env.api.deleteErr = errors.New("delete failed")
	env.forwards.removeErr = errors.New("remove failed")
	env.hosts.writeErrors = []error{nil, errors.New("restore failed")}

	err := env.manager.Enable("awesome-app3/orders", dependencyContext(
		"awesome-app3", "orders", "rabbitmq.default.svc", 5672, 5001,
	))
	if err == nil {
		t.Fatal("expected enable failure")
	}
	for _, text := range []string{
		"manager stream attach failed: attach failed",
		"delete manager session: delete failed",
		"remove port-forwards: remove failed",
		"restore hosts file: restore failed",
	} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("Enable error %q does not contain %q", err, text)
		}
	}
	sessions := env.manager.List()
	if len(sessions) != 1 || sessions[0].State != contracts.HelperSessionStateCleanupPending {
		t.Fatalf("expected cleanup-pending session, got %+v", sessions)
	}
}

func TestDisableRemovesOnlyItsOwnState(t *testing.T) {
	env := newTestEnv(t)
	env.api.createIDs = []string{"mgr-a", "mgr-b"}

	if err := env.manager.Enable("", dependencyContext("proj-a", "svc-a", "rabbitmq.default.svc", 5672, 5001)); err != nil {
		t.Fatalf("enable svc-a failed: %v", err)
	}
	if err := env.manager.Enable("", dependencyContext("proj-b", "svc-b", "redis.default.svc", 6379, 5002)); err != nil {
		t.Fatalf("enable svc-b failed: %v", err)
	}
	streamA := env.streams[0]

	found, err := env.manager.Disable("", dependencyContext("proj-a", "svc-a", "rabbitmq.default.svc", 5672, 5001))
	if err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if !found {
		t.Fatal("expected disable to report the session as found")
	}

	// The hosts file holds one block for every session, so disabling one must
	// leave the other's entries in place.
	wantHosts := []contracts.HostsEntry{{IP: "127.0.0.1", Hostname: "redis.default.svc"}}
	if !slices.Equal(env.hosts.lastWrite, wantHosts) {
		t.Fatalf("unexpected hosts entries after disable: %+v", env.hosts.lastWrite)
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

	found, err := env.manager.Disable("", workerContext())
	if err != nil {
		t.Fatalf("disable of an unknown session returned an error: %v", err)
	}
	if found {
		t.Fatal("expected disable to report no session was found")
	}
}

func TestDisableKeepsSessionWhenTeardownFails(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("enable failed: %v", err)
	}

	env.api.deleteErr = errors.New("manager unreachable")
	found, err := env.manager.Disable("", workerContext())
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
	if _, err := env.manager.Disable("", workerContext()); err != nil {
		t.Fatalf("retried disable failed: %v", err)
	}
	if len(env.manager.List()) != 0 {
		t.Fatalf("expected the retry to remove the session, got %+v", env.manager.List())
	}
}

func TestCloseReleasesLocalStateAndLeavesManagerSessions(t *testing.T) {
	env := newTestEnv(t)
	if err := env.manager.Enable("", workerContext()); err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	stream := env.lastAttach

	if err := env.manager.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	if stream.closes != 1 {
		t.Fatalf("expected the stream to be closed, got %d", stream.closes)
	}
	if env.forwards.clearCalls != 1 {
		t.Fatalf("expected the port-forwards to be cleared, got %d", env.forwards.clearCalls)
	}
	if !env.hosts.removed {
		t.Fatal("expected the krun hosts block to be removed")
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
	go func() { enableDone <- env.manager.Enable("", workerContext()) }()

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
	go func() { enableDone <- env.manager.Enable("", workerContext()) }()
	<-env.attachStarted

	closeDone := make(chan error, 1)
	go func() { closeDone <- env.manager.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before Enable completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(env.attachRelease)
	if err := <-enableDone; err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if env.lastAttach.closes != 1 || env.forwards.clearCalls != 1 || !env.hosts.removed {
		t.Fatalf("Close did not clean completed Enable: stream=%d clear=%d hostsRemoved=%v",
			env.lastAttach.closes, env.forwards.clearCalls, env.hosts.removed)
	}
}

func TestEnableRequiresASessionIdentity(t *testing.T) {
	env := newTestEnv(t)

	if err := env.manager.Enable("", contracts.DebugServiceContext{}); err == nil {
		t.Fatal("expected an error when neither a session key nor a service name is given")
	}
}

func TestPortForwardsForResolvesTargetsAndDeduplicates(t *testing.T) {
	forwards := portForwardsFor(contracts.DebugServiceContext{
		ServiceDependencies: []contracts.DebugServiceDependencyContext{
			{Host: "rabbitmq.default.svc", Port: 5672},
			{Host: "rabbitmq.default.svc", Port: 5672},
			{Host: "ferretdb.ferretdb-system.svc", Port: 27017},
			{Service: "cache", Namespace: "shared", Port: 6379},
			{Host: "no-port.default.svc"},
		},
	})

	want := []contracts.PortForward{
		{Namespace: "default", Service: "rabbitmq", LocalPort: 5672, RemotePort: 5672},
		{Namespace: "ferretdb-system", Service: "ferretdb", LocalPort: 27017, RemotePort: 27017},
		{Namespace: "shared", Service: "cache", LocalPort: 6379, RemotePort: 6379},
	}
	if !slices.Equal(forwards, want) {
		t.Fatalf("unexpected port-forwards:\n got=%+v\nwant=%+v", forwards, want)
	}
}

func TestMergeHostEntriesIncludesAliasesAndDeduplicates(t *testing.T) {
	entries := mergeHostEntries([]contracts.DebugServiceContext{
		{ServiceDependencies: []contracts.DebugServiceDependencyContext{
			{Host: "mongo.default.svc", Port: 27017, Aliases: []string{"mongodb-server-0.mongo.default.svc.cluster.local"}},
		}},
		{ServiceDependencies: []contracts.DebugServiceDependencyContext{
			{Host: "mongo.default.svc", Port: 27017},
			{Host: " ", Port: 1},
		}},
	})

	want := []contracts.HostsEntry{
		{IP: "127.0.0.1", Hostname: "mongo.default.svc"},
		{IP: "127.0.0.1", Hostname: "mongodb-server-0.mongo.default.svc.cluster.local"},
	}
	if !slices.Equal(entries, want) {
		t.Fatalf("unexpected merged hosts entries:\n got=%+v\nwant=%+v", entries, want)
	}
}

func TestResolveKey(t *testing.T) {
	cases := []struct {
		key, project, service, want string
	}{
		{" explicit ", "proj", "svc", "explicit"},
		{"", "proj", "svc", "proj/svc"},
		{"", "", "svc", "svc"},
		{"", "proj", "", "proj"},
		{"", "", "", ""},
	}
	for _, testCase := range cases {
		if got := resolveKey(testCase.key, testCase.project, testCase.service); got != testCase.want {
			t.Fatalf("resolveKey(%q,%q,%q) = %q, want %q", testCase.key, testCase.project, testCase.service, got, testCase.want)
		}
	}
}

func TestNormalizedStateTreatsMissingStateAsActive(t *testing.T) {
	if got := normalizedState(""); got != contracts.HelperSessionStateActive {
		t.Fatalf("missing state = %q, want active", got)
	}
}

func workerContext() contracts.DebugServiceContext {
	return dependencyContext("awesome-app3", "awesome-app3-worker", "rabbitmq.default.svc", 5672, 5001)
}

func dependencyContext(project, service, host string, dependencyPort, interceptPort int) contracts.DebugServiceContext {
	return contracts.DebugServiceContext{
		Project:       project,
		ServiceName:   service,
		ContainerPort: 8080,
		InterceptPort: interceptPort,
		ServiceDependencies: []contracts.DebugServiceDependencyContext{
			{Host: host, Port: dependencyPort},
		},
	}
}

type testEnv struct {
	manager  *Manager
	hosts    *fakeHosts
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
		hosts:    &fakeHosts{},
		forwards: &fakeForwards{},
		api:      &fakeAPI{},
	}
	env.manager = NewManager(Options{
		Hosts:    env.hosts,
		Forwards: env.forwards,
		API:      env.api,
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

type fakeHosts struct {
	lastWrite   []contracts.HostsEntry
	writeErr    error
	writeErrors []error
	removed     bool
}

func (f *fakeHosts) Write(entries []contracts.HostsEntry) error {
	if len(f.writeErrors) > 0 {
		err := f.writeErrors[0]
		f.writeErrors = f.writeErrors[1:]
		if err != nil {
			return err
		}
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	f.lastWrite = append([]contracts.HostsEntry(nil), entries...)
	return nil
}

func (f *fakeHosts) Remove() error {
	f.removed = true
	return nil
}

type fakeForwards struct {
	upsertCalls  int
	removeCalls  int
	clearCalls   int
	lastKey      string
	lastForwards []contracts.PortForward
	upsertErr    error
	removeErr    error
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

func (f *fakeForwards) Remove(sessionKey string) error {
	f.removeCalls++
	f.lastKey = sessionKey
	return f.removeErr
}

func (f *fakeForwards) Clear() error {
	f.clearCalls++
	return nil
}

type fakeAPI struct {
	createCalls   int
	deleteCalls   int
	createIDs     []string
	lastDeletedID string
	createErr     error
	deleteErr     error
}

func (f *fakeAPI) CreateSession(serviceContext contracts.DebugServiceContext) (contracts.DebugSession, error) {
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
		ServiceName:  serviceContext.ServiceName,
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
