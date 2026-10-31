package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/helperipc"
	"github.com/ftechmax/krun/internal/krun-helper/hostfile"
	"github.com/spf13/cobra"
)

// The hosts writer is the only part of krun that runs as root, and it is kept
// deliberately small: it accepts a list of loopback hosts entries on a
// restricted local socket and rewrites krun's block in the machine's hosts
// file. It holds no cluster credentials, opens no outbound connections, and
// knows nothing about debug sessions.
//
// It is long-lived so the developer is prompted for elevation once per
// machine boot rather than once per debug command. Everything else — the
// kubeconfig, the manager token, port-forwards and the traffic stream — lives
// in the unprivileged krun-helper daemon.

func hostsDaemonCommand() *cobra.Command {
	var socketEndpoint string

	command := &cobra.Command{
		Use:   "hosts-daemon",
		Short: "Privileged hosts-file writer (started automatically; requires elevation)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			endpoint := strings.TrimSpace(socketEndpoint)
			if endpoint == "" {
				endpoint = helperipc.HostsEndpoint
			}
			return runHostsDaemon(endpoint)
		},
	}

	command.Flags().StringVar(&socketEndpoint, "socket", "", "IPC endpoint override")
	return command
}

func runHostsDaemon(endpoint string) error {
	listener, err := helperipc.ListenElevated(endpoint)
	if err != nil {
		return fmt.Errorf("listen on hosts endpoint: %w", err)
	}
	defer helperipc.Cleanup(endpoint)

	shutdownCh := make(chan struct{}, 1)
	writer := &hostsWriter{}
	server := &http.Server{
		Handler:           newHostsHandler(writer, shutdownCh),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErrCh := make(chan error, 1)
	go func() { serverErrCh <- server.Serve(listener) }()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	fmt.Printf("krun hosts writer %s listening on %s\n", version, endpoint)

	select {
	case err := <-serverErrCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-signalCh:
	case <-shutdownCh:
	}

	return shutdownHostsDaemon(server, serverErrCh, writer)
}

// shutdownHostsDaemon drains in-flight requests before stripping krun's block,
// so a request that was already being handled cannot write the block back.
// The writer is closed after draining, so a handler that outlives the drain
// timeout is refused rather than writing after the block is gone.
func shutdownHostsDaemon(server *http.Server, serverErrCh <-chan error, writer *hostsWriter) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if serveErr := <-serverErrCh; !errors.Is(serveErr, http.ErrServerClosed) && serveErr != nil {
		shutdownErr = errors.Join(shutdownErr, serveErr)
	}

	// Leaving krun's block behind would point dependency names at a machine
	// with nothing listening, so strip it on the way out.
	return errors.Join(writer.closeAndRemove(), shutdownErr)
}

// hostsWriter serializes hosts-file updates. It drops snapshots older than
// the last one applied (concurrent CLI commands can deliver them out of
// order) and refuses updates once shutdown has stripped the block.
type hostsWriter struct {
	mu     sync.Mutex
	last   contracts.HostsRevision
	closed bool
}

var (
	errHostsWriterClosed = errors.New("hosts writer is shutting down")
	errStaleHosts        = errors.New("a newer hosts snapshot is already applied")
)

func (h *hostsWriter) apply(entries []contracts.HostsEntry, revision contracts.HostsRevision) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return errHostsWriterClosed
	}
	if revision.OlderThan(h.last) {
		return errStaleHosts
	}

	var err error
	if len(entries) == 0 {
		err = hostfile.Remove()
	} else {
		err = hostfile.Write(entries)
	}
	if err != nil {
		return err
	}
	h.last = revision
	return nil
}

func (h *hostsWriter) closeAndRemove() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closed = true
	return hostfile.Remove()
}

func newHostsHandler(writer *hostsWriter, shutdownCh chan<- struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "ok", Version: version})
	})
	mux.HandleFunc("POST /v1/hosts", writer.handleApply)
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "shutting down"})
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	})
	return mux
}

func (h *hostsWriter) handleApply(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var request contracts.ApplyHostsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{Success: false, Message: "invalid payload"})
		return
	}

	if err := hostfile.Validate(request.Entries); err != nil {
		writeJSON(w, http.StatusBadRequest, contracts.HelperResponse{Success: false, Message: err.Error()})
		return
	}

	err := h.apply(request.Entries, request.Revision)
	switch {
	case errors.Is(err, errStaleHosts):
		// Not a failure: the hosts file already reflects a newer state.
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "stale snapshot ignored"})
	case errors.Is(err, errHostsWriterClosed):
		writeJSON(w, http.StatusServiceUnavailable, contracts.HelperResponse{Success: false, Message: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{Success: false, Message: err.Error()})
	default:
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "hosts updated"})
	}
}
