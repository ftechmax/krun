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
	defaultAgentListenPort = 8081
	defaultStreamPath      = "/v1/stream/agent"
	maxBackoff             = 10 * time.Second
	initialBackoff         = time.Second
	streamQueueSize        = 2048
)

type runtimeConfig struct {
	SessionID       string
	SessionToken    string
	ManagerAddress  string
	TargetPort      int
	AgentListenPort int
	StreamURL       string
}

type reconnectingStreamClient struct {
	streamURL string
	headers   http.Header
	sendCh    chan contracts.StreamEnvelope
	onReceive func(contracts.StreamEnvelope)
	cancel    context.CancelFunc
	doneCh    chan struct{}
	once      sync.Once
}

type connectionRegistry struct {
	mu    sync.Mutex
	conns map[string]net.Conn
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

	connectionRegistry := newConnectionRegistry()
	defer connectionRegistry.CloseAll()

	streamClient := newReconnectingStreamClient(
		ctx,
		cfg,
		func(envelope contracts.StreamEnvelope) {
			handleInboundEnvelope(connectionRegistry, envelope)
		},
	)
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

	var connIDCounter atomic.Uint64
	go acceptInterceptedConnections(ctx, listener, cfg, streamClient, connectionRegistry, &connIDCounter)

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
	connectionRegistry *connectionRegistry,
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

		go captureConnection(ctx, conn, cfg, streamClient, connectionRegistry, connIDCounter)
	}
}

func captureConnection(
	ctx context.Context,
	conn net.Conn,
	cfg runtimeConfig,
	streamClient *reconnectingStreamClient,
	connectionRegistry *connectionRegistry,
	connIDCounter *atomic.Uint64,
) {
	defer conn.Close()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	connectionID := fmt.Sprintf("%s-%d", agentID, connIDCounter.Add(1))
	connectionRegistry.Set(connectionID, conn)
	defer connectionRegistry.CloseAndDelete(connectionID)

	openEnvelope := cfg.newStreamEnvelope(connectionID, contracts.StreamTypeOpen)
	openEnvelope.Metadata = map[string]string{
		"remote_addr": conn.RemoteAddr().String(),
		"local_addr":  conn.LocalAddr().String(),
	}
	if err := streamClient.Send(ctx, openEnvelope); err != nil {
		log.Printf("send open envelope failed (connection_id=%s): %v", connectionID, err)
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
				return
			}
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			if err := streamClient.Send(ctx, cfg.newStreamEnvelope(connectionID, contracts.StreamTypeClose)); err != nil {
				log.Printf("send close envelope failed after EOF (connection_id=%s): %v", connectionID, err)
			}
			return
		}

		errorEnvelope := cfg.newStreamEnvelope(connectionID, contracts.StreamTypeError)
		errorEnvelope.Message = readErr.Error()
		if err := streamClient.Send(ctx, errorEnvelope); err != nil {
			log.Printf("send error envelope failed (connection_id=%s): %v", connectionID, err)
		}
		if err := streamClient.Send(ctx, cfg.newStreamEnvelope(connectionID, contracts.StreamTypeClose)); err != nil {
			log.Printf("send close envelope failed (connection_id=%s): %v", connectionID, err)
		}
		return
	}
}

func newReconnectingStreamClient(
	parent context.Context,
	cfg runtimeConfig,
	onReceive func(contracts.StreamEnvelope),
) *reconnectingStreamClient {
	ctx, cancel := context.WithCancel(parent) //nolint:gosec // Cancel func is stored and called by Close.
	headers := make(http.Header)
	headers.Set("X-Krun-Session-ID", cfg.SessionID)
	if strings.TrimSpace(cfg.SessionToken) != "" {
		headers.Set("X-Krun-Session-Token", cfg.SessionToken)
	}

	client := &reconnectingStreamClient{
		streamURL: cfg.StreamURL,
		headers:   headers,
		sendCh:    make(chan contracts.StreamEnvelope, streamQueueSize),
		onReceive: onReceive,
		cancel:    cancel,
		doneCh:    make(chan struct{}),
	}

	go client.run(ctx)
	return client
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
	}
}

func (c *reconnectingStreamClient) pumpConnection(ctx context.Context, conn *websocket.Conn) error {
	defer conn.Close()

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
	cmd := exec.CommandContext(ctx, "iptables", args...) //nolint:gosec // Binary is fixed; args are built internally.
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

func newConnectionRegistry() *connectionRegistry {
	return &connectionRegistry{
		conns: map[string]net.Conn{},
	}
}

func (r *connectionRegistry) Set(connectionID string, conn net.Conn) {
	r.mu.Lock()
	if previous, ok := r.conns[connectionID]; ok {
		_ = previous.Close()
	}
	r.conns[connectionID] = conn
	r.mu.Unlock()
}

func (r *connectionRegistry) Get(connectionID string) net.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conns[connectionID]
}

func (r *connectionRegistry) CloseAndDelete(connectionID string) {
	if strings.TrimSpace(connectionID) == "" {
		return
	}
	r.mu.Lock()
	conn, ok := r.conns[connectionID]
	if ok {
		delete(r.conns, connectionID)
	}
	r.mu.Unlock()
	if ok {
		_ = conn.Close()
	}
}

func (r *connectionRegistry) CloseAll() {
	r.mu.Lock()
	conns := make([]net.Conn, 0, len(r.conns))
	for _, conn := range r.conns {
		conns = append(conns, conn)
	}
	r.conns = map[string]net.Conn{}
	r.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func handleInboundEnvelope(connectionRegistry *connectionRegistry, envelope contracts.StreamEnvelope) {
	connectionID := strings.TrimSpace(envelope.ConnectionID)
	if connectionID == "" {
		return
	}

	switch envelope.Type {
	case contracts.StreamTypeData:
		if len(envelope.Data) == 0 {
			return
		}
		conn := connectionRegistry.Get(connectionID)
		if conn == nil {
			return
		}
		if _, err := conn.Write(envelope.Data); err != nil {
			connectionRegistry.CloseAndDelete(connectionID)
		}
	case contracts.StreamTypeClose, contracts.StreamTypeError:
		connectionRegistry.CloseAndDelete(connectionID)
	}
}
