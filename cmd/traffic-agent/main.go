package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

var version = "debug"

const (
	envManagerAddress  = "KRUN_MANAGER_ADDRESS"
	envSessionID       = "KRUN_SESSION_ID"
	envSessionToken    = "KRUN_SESSION_TOKEN"
	envTargetPort      = "KRUN_TARGET_PORT"
	envAgentListenPort = "KRUN_AGENT_LISTEN_PORT"

	defaultAgentListenPort = 8081
	streamPath             = "/v1/stream/agent"
	initialBackoff         = time.Second
	maxBackoff             = 10 * time.Second
)

type config struct {
	SessionID    string
	SessionToken string
	TargetPort   int
	ListenPort   int
	StreamURL    string
}

func main() {
	if err := run(); err != nil {
		slog.Error("traffic-agent failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	slog.Info("traffic-agent starting",
		"version", version,
		"session_id", cfg.SessionID,
		"target_port", cfg.TargetPort,
		"listen_port", cfg.ListenPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := installRedirect(ctx, cfg.TargetPort, cfg.ListenPort); err != nil {
		return fmt.Errorf("install redirect rule: %w", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := removeRedirect(cleanupCtx, cfg.TargetPort, cfg.ListenPort); err != nil {
			slog.Error("cleanup redirect rule failed", "err", err)
		}
	}()

	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.ListenPort)))
	if err != nil {
		return fmt.Errorf("listen on intercept port %d: %w", cfg.ListenPort, err)
	}
	defer listener.Close()

	var session atomic.Pointer[yamux.Session]
	go acceptLoop(ctx, listener, &session)
	go dialLoop(ctx, cfg, &session)

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	sig := <-signalCh
	slog.Info("received signal, shutting down traffic-agent", "signal", sig.String())
	cancel()
	if s := session.Load(); s != nil {
		_ = s.Close()
	}
	_ = listener.Close()
	return nil
}

func loadConfig() (config, error) {
	sessionID := strings.TrimSpace(os.Getenv(envSessionID))
	if sessionID == "" {
		return config{}, fmt.Errorf("%s is required", envSessionID)
	}
	managerAddress := strings.TrimSpace(os.Getenv(envManagerAddress))
	if managerAddress == "" {
		return config{}, fmt.Errorf("%s is required", envManagerAddress)
	}
	targetPort, err := parsePort(envTargetPort, 0)
	if err != nil {
		return config{}, err
	}
	listenPort, err := parsePort(envAgentListenPort, defaultAgentListenPort)
	if err != nil {
		return config{}, err
	}
	streamURL, err := buildStreamURL(managerAddress, sessionID)
	if err != nil {
		return config{}, err
	}
	return config{
		SessionID:    sessionID,
		SessionToken: strings.TrimSpace(os.Getenv(envSessionToken)),
		TargetPort:   targetPort,
		ListenPort:   listenPort,
		StreamURL:    streamURL,
	}, nil
}

func parsePort(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		if fallback == 0 {
			return 0, fmt.Errorf("%s is required", name)
		}
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be a valid port (1-65535)", name)
	}
	return port, nil
}

func buildStreamURL(managerAddress, sessionID string) (string, error) {
	if !strings.Contains(managerAddress, "://") {
		managerAddress = "http://" + managerAddress
	}
	u, err := url.Parse(managerAddress)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid manager address %q", managerAddress)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported manager address scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + streamPath
	q := u.Query()
	q.Set("session_id", sessionID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func dialLoop(ctx context.Context, cfg config, session *atomic.Pointer[yamux.Session]) {
	headers := http.Header{}
	headers.Set("X-Krun-Session-ID", cfg.SessionID)
	if cfg.SessionToken != "" {
		headers.Set("X-Krun-Session-Token", cfg.SessionToken)
	}

	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}

		ws, _, err := websocket.Dial(ctx, cfg.StreamURL, &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			slog.Error("manager stream connect failed", "err", err)
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		yamuxCfg := yamux.DefaultConfig()
		yamuxCfg.LogOutput = io.Discard
		mux, err := yamux.Client(websocket.NetConn(ctx, ws, websocket.MessageBinary), yamuxCfg)
		if err != nil {
			slog.Error("yamux client init failed", "err", err)
			_ = ws.Close(websocket.StatusInternalError, "")
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		slog.Info("manager stream connected")
		backoff = initialBackoff
		session.Store(mux)

		<-mux.CloseChan()
		session.CompareAndSwap(mux, nil)
		slog.Info("manager stream disconnected")
	}
}

func acceptLoop(ctx context.Context, listener net.Listener, session *atomic.Pointer[yamux.Session]) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			slog.Error("accept failed", "err", err)
			continue
		}
		go forward(conn, session)
	}
}

func forward(local net.Conn, session *atomic.Pointer[yamux.Session]) {
	defer local.Close()
	mux := session.Load()
	if mux == nil || mux.IsClosed() {
		return
	}
	stream, err := mux.Open()
	if err != nil {
		slog.Error("open stream failed", "err", err)
		return
	}
	defer stream.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, stream); done <- struct{}{} }()
	<-done
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxBackoff {
		return maxBackoff
	}
	return next
}

func installRedirect(ctx context.Context, targetPort, listenPort int) error {
	args := buildRedirectArgs(targetPort, listenPort)
	check := append([]string{"-t", "nat", "-C", "PREROUTING"}, args...)
	if err := runIptables(ctx, check...); err == nil {
		return nil
	}
	add := append([]string{"-t", "nat", "-A", "PREROUTING"}, args...)
	if err := runIptables(ctx, add...); err != nil {
		return err
	}
	slog.Info("installed iptables redirect", "target_port", targetPort, "listen_port", listenPort)
	return nil
}

func removeRedirect(ctx context.Context, targetPort, listenPort int) error {
	args := append([]string{"-t", "nat", "-D", "PREROUTING"}, buildRedirectArgs(targetPort, listenPort)...)
	if err := runIptables(ctx, args...); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "bad rule") || strings.Contains(msg, "no chain/target/match by that name") {
			return nil
		}
		return err
	}
	slog.Info("removed iptables redirect", "target_port", targetPort, "listen_port", listenPort)
	return nil
}

func buildRedirectArgs(targetPort, listenPort int) []string {
	return []string{
		"-p", "tcp",
		"--dport", strconv.Itoa(targetPort),
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(listenPort),
	}
}

func runIptables(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "iptables", args...) //nolint:gosec
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("iptables %s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("iptables %s: %w: %s", strings.Join(args, " "), err, message)
}
