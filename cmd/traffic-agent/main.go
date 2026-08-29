package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/streamlink"
	"github.com/google/uuid"
)

var (
	version = "debug" // will be set by the build system
	agentID = uuid.NewString()
)

const (
	managerAddressEnv      = "KRUN_MANAGER_ADDRESS"
	sessionIDEnv           = "KRUN_SESSION_ID"
	sessionTokenEnv        = "KRUN_SESSION_TOKEN"
	targetPortEnv          = "KRUN_TARGET_PORT"
	agentListenPortEnv     = "KRUN_AGENT_LISTEN_PORT"
	agentProbePortEnv      = "KRUN_AGENT_PROBE_PORT"
	defaultAgentListenPort = 8081
	defaultAgentProbePort  = 8082
)

type runtimeConfig struct {
	SessionID       string
	SessionToken    string
	ManagerAddress  string
	TargetPort      int
	AgentListenPort int
	ProbePort       int
	StreamURL       string
}

type redirectRule struct {
	TargetPort  int
	ListenPort  int
	RunIptables func(ctx context.Context, args ...string) error
}

func main() {
	if err := run(); err != nil {
		log.Printf("traffic-agent failed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	log.Printf(
		"krun traffic-agent %s starting (session_id=%q target_port=%d listen_port=%d manager=%q)",
		version,
		cfg.SessionID,
		cfg.TargetPort,
		cfg.AgentListenPort,
		cfg.ManagerAddress,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conns := streamlink.NewConns()
	defer conns.CloseAll()

	link := streamlink.New(ctx, streamlink.Config{
		URL:       cfg.StreamURL,
		Headers:   cfg.streamHeaders(),
		SessionID: cfg.SessionID,
		LogPrefix: "manager stream",
	})
	// The manager drops connection routing for this session when the agent
	// stream detaches (and tells the helper to close its side), so every
	// intercepted conn from the previous epoch is dead: close them instead
	// of streaming into the void after reconnect.
	link.Start(func(envelope contracts.StreamEnvelope) {
		streamlink.ApplyInbound(link, conns, envelope)
	}, conns.CloseAll)
	defer link.Close()

	rule := redirectRule{
		TargetPort: cfg.TargetPort,
		ListenPort: cfg.AgentListenPort,
	}
	if err := rule.Install(ctx); err != nil {
		return fmt.Errorf("install redirect rule: %w", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := rule.Remove(cleanupCtx); err != nil {
			log.Printf("cleanup redirect rule failed: %v", err)
		}
	}()

	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(cfg.AgentListenPort)))
	if err != nil {
		return fmt.Errorf("listen on intercept port %d: %w", cfg.AgentListenPort, err)
	}
	defer listener.Close()

	// Kubelet probes that originally targeted the intercepted port are
	// rewritten by the injector to point here; answering them keeps the pod
	// Ready while the developer's local app is stopped or on a breakpoint.
	probeServer, err := startProbeServer(cfg.ProbePort)
	if err != nil {
		return err
	}
	defer probeServer.Close()

	go acceptInterceptedConnections(ctx, listener, link, conns)

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signalCh)

	sig := <-signalCh
	log.Printf("received %s, shutting down traffic-agent", sig)
	cancel()
	_ = listener.Close()
	return nil
}

func loadRuntimeConfig() (runtimeConfig, error) {
	sessionID := strings.TrimSpace(os.Getenv(sessionIDEnv))
	if sessionID == "" {
		return runtimeConfig{}, fmt.Errorf("%s is required", sessionIDEnv)
	}

	managerAddress := resolveManagerAddress()
	if managerAddress == "" {
		return runtimeConfig{}, fmt.Errorf("manager address is required (%s)", managerAddressEnv)
	}

	targetPort, err := parseRequiredPort(targetPortEnv)
	if err != nil {
		return runtimeConfig{}, err
	}

	agentListenPort, err := parseOptionalPort(agentListenPortEnv, defaultAgentListenPort)
	if err != nil {
		return runtimeConfig{}, err
	}

	probePort, err := parseOptionalPort(agentProbePortEnv, defaultAgentProbePort)
	if err != nil {
		return runtimeConfig{}, err
	}

	// Keep the session token out of the URL, where access logs commonly record it.
	streamURL, err := streamlink.URL(managerAddress, streamlink.AgentPath, sessionID)
	if err != nil {
		return runtimeConfig{}, err
	}

	return runtimeConfig{
		SessionID:       sessionID,
		SessionToken:    strings.TrimSpace(os.Getenv(sessionTokenEnv)),
		ManagerAddress:  managerAddress,
		TargetPort:      targetPort,
		AgentListenPort: agentListenPort,
		ProbePort:       probePort,
		StreamURL:       streamURL,
	}, nil
}

func (cfg runtimeConfig) streamHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("X-Krun-Session-ID", cfg.SessionID)
	headers.Set("X-Krun-Agent-Version", version)
	if strings.TrimSpace(cfg.SessionToken) != "" {
		headers.Set("X-Krun-Session-Token", cfg.SessionToken)
	}
	return headers
}

func resolveManagerAddress() string {
	return strings.TrimSpace(os.Getenv(managerAddressEnv))
}

func parseRequiredPort(name string) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	return parsePort(name, value)
}

func parseOptionalPort(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	return parsePort(name, value)
}

func parsePort(name string, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer port", name)
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be in range 1-65535", name)
	}
	return port, nil
}

func acceptInterceptedConnections(ctx context.Context, listener net.Listener, link *streamlink.Link, conns *streamlink.Conns) {
	var connIDCounter atomic.Uint64
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			log.Printf("accept intercepted connection failed: %v", err)
			continue
		}

		connectionID := fmt.Sprintf("%s-%d", agentID, connIDCounter.Add(1))
		go captureConnection(ctx, conn, connectionID, link, conns)
	}
}

func captureConnection(ctx context.Context, conn net.Conn, connectionID string, link *streamlink.Link, conns *streamlink.Conns) {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	conns.Set(connectionID, conn)

	openEnvelope := link.Envelope(connectionID, contracts.StreamTypeOpen)
	openEnvelope.Metadata = map[string]string{
		"remote_addr": conn.RemoteAddr().String(),
		"local_addr":  conn.LocalAddr().String(),
	}
	if err := link.Send(openEnvelope); err != nil {
		log.Printf("send open envelope failed (connection_id=%s): %v", connectionID, err)
		conns.CloseAndDelete(connectionID)
		return
	}

	streamlink.PumpConn(link, conns, connectionID, conn)
}

// startProbeServer answers rewritten kubelet probes: accepting the TCP
// connection satisfies tcpSocket probes, and any HTTP request gets 200 OK
// for httpGet probes.
func startProbeServer(port int) (*http.Server, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		return nil, fmt.Errorf("listen on probe port %d: %w", port, err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("probe server stopped: %v", err)
		}
	}()
	log.Printf("probe server listening on :%d", port)
	return server, nil
}

func runIptablesCommand(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "iptables", args...)
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

func (r redirectRule) Install(ctx context.Context) error {
	runIptables := r.runner()

	redirectArgs := buildRedirectArgs(r.TargetPort, r.ListenPort)
	checkArgs := append([]string{"-t", "nat", "-C", "PREROUTING"}, redirectArgs...)
	if err := runIptables(ctx, checkArgs...); err == nil {
		return nil
	}

	addArgs := append([]string{"-t", "nat", "-A", "PREROUTING"}, redirectArgs...)
	if err := runIptables(ctx, addArgs...); err != nil {
		return err
	}
	log.Printf("installed iptables redirect target_port=%d listen_port=%d", r.TargetPort, r.ListenPort)
	return nil
}

func (r redirectRule) Remove(ctx context.Context) error {
	removeArgs := append([]string{"-t", "nat", "-D", "PREROUTING"}, buildRedirectArgs(r.TargetPort, r.ListenPort)...)
	if err := r.runner()(ctx, removeArgs...); err != nil {
		if isMissingIptablesRuleError(err) {
			return nil
		}
		return err
	}
	log.Printf("removed iptables redirect target_port=%d listen_port=%d", r.TargetPort, r.ListenPort)
	return nil
}

func (r redirectRule) runner() func(ctx context.Context, args ...string) error {
	if r.RunIptables != nil {
		return r.RunIptables
	}
	return runIptablesCommand
}

func buildRedirectArgs(targetPort int, listenPort int) []string {
	return []string{
		"-p", "tcp",
		"--dport", strconv.Itoa(targetPort),
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(listenPort),
	}
}

func isMissingIptablesRuleError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "bad rule") ||
		strings.Contains(message, "no chain/target/match by that name")
}
