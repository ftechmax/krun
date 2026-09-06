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
	server := &http.Server{
		Handler:           newHostsHandler(shutdownCh),
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

	// Leaving krun's block behind would point dependency names at a machine
	// with nothing listening, so strip it on the way out.
	removeErr := hostfile.File{}.Remove()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if serveErr := <-serverErrCh; !errors.Is(serveErr, http.ErrServerClosed) && serveErr != nil {
		shutdownErr = errors.Join(shutdownErr, serveErr)
	}
	return errors.Join(removeErr, shutdownErr)
}

func newHostsHandler(shutdownCh chan<- struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", only(http.MethodGet, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "ok", Version: version})
	}))
	mux.HandleFunc("/v1/hosts", only(http.MethodPost, handleApplyHosts))
	mux.HandleFunc("/v1/shutdown", only(http.MethodPost, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "shutting down"})
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	}))
	return mux
}

func handleApplyHosts(w http.ResponseWriter, r *http.Request) {
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

	var err error
	if len(request.Entries) == 0 {
		err = hostfile.File{}.Remove()
	} else {
		err = hostfile.File{}.Write(request.Entries)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, contracts.HelperResponse{Success: false, Message: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, contracts.HelperResponse{Success: true, Message: "hosts updated"})
}
