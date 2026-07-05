package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/streamconn"
	"github.com/ftechmax/krun/internal/wskeepalive"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
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
	defaultStreamPath      = "/v1/stream/agent"
	maxBackoff             = 10 * time.Second
	initialBackoff         = time.Second
	streamQueueSize        = 2048
	connWriteTimeout       = 10 * time.Second
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

type reconnectingStreamClient struct {
	streamURL    string
	headers      http.Header
	sendCh       chan contracts.StreamEnvelope
	onReceive    func(contracts.StreamEnvelope)
	onDisconnect func()
	runCtx       context.Context
	cancel       context.CancelFunc
	doneCh       chan struct{}
	once         sync.Once
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

	connections := streamconn.NewRegistry()
	defer connections.CloseAll()

	// The manager drops connection routing for this session when the agent
	// stream detaches (and tells the helper to close its side), so every
	// intercepted conn from the previous epoch is dead: close them instead
	// of streaming into the void after reconnect.
	streamClient := newReconnectingStreamClient(ctx, cfg, nil, connections.CloseAll)
	streamClient.onReceive = func(envelope contracts.StreamEnvelope) {
		handleInboundEnvelope(connections, envelope, func(connectionID string) {
			_ = streamClient.Send(ctx, cfg.newStreamEnvelope(connectionID, contracts.StreamTypeClose))
		})
	}
	streamClient.start()
	defer streamClient.Close()

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

	var connIDCounter atomic.Uint64
	go acceptInterceptedConnections(ctx, listener, cfg, streamClient, connections, &connIDCounter)

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

	streamURL, err := buildStreamURL(managerAddress, sessionID)
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

func (cfg runtimeConfig) newStreamEnvelope(connectionID string, envelopeType string) contracts.StreamEnvelope {
	return contracts.StreamEnvelope{
		Type:         envelopeType,
		SessionID:    cfg.SessionID,
		SessionToken: cfg.SessionToken,
		ConnectionID: connectionID,
	}
}

func resolveManagerAddress() string {
	return strings.TrimSpace(os.Getenv(managerAddressEnv))
}

func parseRequiredPort(name string) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer port", name)
	}
	if err := validatePort(port); err != nil {
		return 0, fmt.Errorf("%s %w", name, err)
	}
	return port, nil
}

func parseOptionalPort(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer port", name)
	}
	if err := validatePort(port); err != nil {
		return 0, fmt.Errorf("%s %w", name, err)
	}
	return port, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("must be in range 1-65535")
	}
	return nil
}

func buildStreamURL(managerAddress string, sessionID string) (string, error) {
	rawAddress := strings.TrimSpace(managerAddress)
	if rawAddress == "" {
		return "", errors.New("manager address is required")
	}
	if !strings.Contains(rawAddress, "://") {
		rawAddress = "http://" + rawAddress
	}

	parsed, err := url.Parse(rawAddress)
	if err != nil {
		return "", fmt.Errorf("invalid manager address %q: %w", managerAddress, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid manager address %q: host is required", managerAddress)
	}

	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported manager address scheme %q", parsed.Scheme)
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	parsed.Path = basePath + defaultStreamPath
	query := parsed.Query()
	query.Set("session_id", strings.TrimSpace(sessionID))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func acceptInterceptedConnections(
	ctx context.Context,
	listener net.Listener,
	cfg runtimeConfig,
	streamClient *reconnectingStreamClient,
	connections *streamconn.Registry,
	connIDCounter *atomic.Uint64,
) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			log.Printf("accept intercepted connection failed: %v", err)
			continue
		}

		go captureConnection(ctx, conn, cfg, streamClient, connections, connIDCounter)
	}
}

func captureConnection(
	ctx context.Context,
	conn net.Conn,
	cfg runtimeConfig,
	streamClient *reconnectingStreamClient,
	connections *streamconn.Registry,
	connIDCounter *atomic.Uint64,
) {
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	connectionID := fmt.Sprintf("%s-%d", agentID, connIDCounter.Add(1))
	connections.Set(connectionID, conn)

	openEnvelope := cfg.newStreamEnvelope(connectionID, contracts.StreamTypeOpen)
	openEnvelope.Metadata = map[string]string{
		"remote_addr": conn.RemoteAddr().String(),
		"local_addr":  conn.LocalAddr().String(),
	}
	if err := streamClient.Send(ctx, openEnvelope); err != nil {
		log.Printf("send open envelope failed (connection_id=%s): %v", connectionID, err)
		connections.CloseAndDelete(connectionID)
		return
	}

	buffer := make([]byte, 32*1024)
	for {
		readBytes, readErr := conn.Read(buffer)
		if readBytes > 0 {
			chunk := make([]byte, readBytes)
			copy(chunk, buffer[:readBytes])
			dataEnvelope := cfg.newStreamEnvelope(connectionID, contracts.StreamTypeData)
			dataEnvelope.Data = chunk
			if err := streamClient.Send(ctx, dataEnvelope); err != nil {
				log.Printf("send data envelope failed (connection_id=%s): %v", connectionID, err)
				connections.CloseAndDelete(connectionID)
				return
			}
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			// Half-close: the caller finished sending but may still expect
			// response data, so keep the conn registered for writes.
			_ = streamClient.Send(ctx, cfg.newStreamEnvelope(connectionID, contracts.StreamTypeCloseWrite))
			if connections.MarkReadClosed(connectionID) {
				_ = streamClient.Send(ctx, cfg.newStreamEnvelope(connectionID, contracts.StreamTypeClose))
			}
			return
		}

		errorEnvelope := cfg.newStreamEnvelope(connectionID, contracts.StreamTypeError)
		errorEnvelope.Message = readErr.Error()
		_ = streamClient.Send(ctx, errorEnvelope)
		_ = streamClient.Send(ctx, cfg.newStreamEnvelope(connectionID, contracts.StreamTypeClose))
		connections.CloseAndDelete(connectionID)
		return
	}
}

func newReconnectingStreamClient(
	parent context.Context,
	cfg runtimeConfig,
	onReceive func(contracts.StreamEnvelope),
	onDisconnect func(),
) *reconnectingStreamClient {
	ctx, cancel := context.WithCancel(parent)
	headers := make(http.Header)
	headers.Set("X-Krun-Session-ID", cfg.SessionID)
	if strings.TrimSpace(cfg.SessionToken) != "" {
		headers.Set("X-Krun-Session-Token", cfg.SessionToken)
	}

	client := &reconnectingStreamClient{
		streamURL:    cfg.StreamURL,
		headers:      headers,
		sendCh:       make(chan contracts.StreamEnvelope, streamQueueSize),
		onReceive:    onReceive,
		onDisconnect: onDisconnect,
		runCtx:       ctx,
		cancel:       cancel,
		doneCh:       make(chan struct{}),
	}
	return client
}

// start launches the reconnect loop. Kept separate from construction so the
// caller can finish wiring callbacks that reference the client itself.
func (c *reconnectingStreamClient) start() {
	go c.run(c.runCtx)
}

func (c *reconnectingStreamClient) Send(ctx context.Context, envelope contracts.StreamEnvelope) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.doneCh:
		return errors.New("stream client is closed")
	case c.sendCh <- envelope:
		return nil
	}
}

func (c *reconnectingStreamClient) Close() {
	c.once.Do(func() {
		c.cancel()
		<-c.doneCh
	})
}

func (c *reconnectingStreamClient) run(ctx context.Context) {
	defer close(c.doneCh)

	backoff := initialBackoff
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.streamURL, c.headers)
		if err != nil {
			log.Printf("manager stream connect failed: %v", err)
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		backoff = initialBackoff
		log.Printf("manager stream connected")
		err = c.pumpConnection(ctx, conn)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("manager stream disconnected: %v", err)
		}
		if c.onDisconnect != nil {
			c.onDisconnect()
		}
	}
}

func (c *reconnectingStreamClient) pumpConnection(ctx context.Context, conn *websocket.Conn) error {
	defer conn.Close()

	wskeepalive.Configure(conn)
	pingDone := make(chan struct{})
	defer close(pingDone)
	go wskeepalive.Ping(conn, pingDone)

	readerErrCh := make(chan error, 1)
	inboundCh := make(chan contracts.StreamEnvelope, 128)
	go func() {
		for {
			var envelope contracts.StreamEnvelope
			if err := conn.ReadJSON(&envelope); err != nil {
				readerErrCh <- err
				return
			}
			inboundCh <- envelope
		}
	}()

	var pending *contracts.StreamEnvelope
	for {
		if pending == nil {
			select {
			case <-ctx.Done():
				return nil
			case readErr := <-readerErrCh:
				return readErr
			case inbound := <-inboundCh:
				if c.onReceive != nil {
					c.onReceive(inbound)
				}
			case envelope := <-c.sendCh:
				pending = &envelope
			}
			if pending == nil {
				continue
			}
		}

		if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return err
		}
		if err := conn.WriteJSON(*pending); err != nil {
			return err
		}
		pending = nil
	}
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
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("probe server stopped: %v", err)
		}
	}()
	log.Printf("probe server listening on :%d", port)
	return server, nil
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
	runIptables := r.RunIptables
	if runIptables == nil {
		runIptables = runIptablesCommand
	}

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
	runIptables := r.RunIptables
	if runIptables == nil {
		runIptables = runIptablesCommand
	}

	removeArgs := append([]string{"-t", "nat", "-D", "PREROUTING"}, buildRedirectArgs(r.TargetPort, r.ListenPort)...)
	if err := runIptables(ctx, removeArgs...); err != nil {
		if isMissingIptablesRuleError(err) {
			return nil
		}
		return err
	}
	log.Printf("removed iptables redirect target_port=%d listen_port=%d", r.TargetPort, r.ListenPort)
	return nil
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

func handleInboundEnvelope(connections *streamconn.Registry, envelope contracts.StreamEnvelope, sendClose func(connectionID string)) {
	connectionID := strings.TrimSpace(envelope.ConnectionID)
	if connectionID == "" {
		return
	}

	switch envelope.Type {
	case contracts.StreamTypeData:
		if len(envelope.Data) == 0 {
			return
		}
		conn := connections.Get(connectionID)
		if conn == nil {
			return
		}
		// Bound the write so a caller that stopped reading cannot stall the
		// stream pump for every other intercepted connection.
		_ = conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		if _, err := conn.Write(envelope.Data); err != nil {
			connections.CloseAndDelete(connectionID)
		}
	case contracts.StreamTypeCloseWrite:
		if connections.CloseWriteHalf(connectionID) && sendClose != nil {
			sendClose(connectionID)
		}
	case contracts.StreamTypeClose, contracts.StreamTypeError:
		connections.CloseAndDelete(connectionID)
	}
}
