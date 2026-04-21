package trafficmanagerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/kube"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	defaultManagerNamespace   = "krun-system"
	defaultManagerServiceName = "krun-traffic-manager"
	defaultManagerServicePort = 8080
	ManagerClientID           = "krun-helper"
	managerRequestTimeout     = 10 * time.Second
)

func CreateSession(kubeConfigPath string, ctx contracts.DebugServiceContext) (contracts.DebugSession, error) {
	serviceName := strings.TrimSpace(ctx.ServiceName)
	if serviceName == "" {
		return contracts.DebugSession{}, errors.New("create manager session: service_name is required")
	}
	if ctx.ContainerPort <= 0 {
		return contracts.DebugSession{}, errors.New("create manager session: service_port must be greater than 0")
	}
	if ctx.InterceptPort <= 0 {
		return contracts.DebugSession{}, errors.New("create manager session: local_port must be greater than 0")
	}

	request := contracts.CreateDebugSessionRequest{
		Namespace:   NormalizeNamespace(ctx.Namespace),
		ServiceName: serviceName,
		Workload:    serviceName,
		ServicePort: ctx.ContainerPort,
		LocalPort:   ctx.InterceptPort,
		ClientID:    ManagerClientID,
	}

	body, err := json.Marshal(request)
	if err != nil {
		return contracts.DebugSession{}, fmt.Errorf("marshal manager create request: %w", err)
	}

	client, err := kube.NewClient(kubeConfigPath)
	if err != nil {
		return contracts.DebugSession{}, fmt.Errorf("create kubernetes client: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(context.Background(), managerRequestTimeout)
	defer cancel()

	responseBody, err := client.Clientset.CoreV1().RESTClient().Post().
		Namespace(defaultManagerNamespace).
		Resource("services").
		Name(serviceProxyName()).
		SubResource("proxy").
		Suffix("v1", "sessions").
		Body(body).
		Do(requestCtx).
		Raw()
	if err != nil {
		return contracts.DebugSession{}, fmt.Errorf("create manager session: %w", err)
	}

	var session contracts.DebugSession
	if err := json.Unmarshal(responseBody, &session); err != nil {
		return contracts.DebugSession{}, fmt.Errorf("decode manager create response: %w", err)
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return contracts.DebugSession{}, fmt.Errorf("create manager session: empty session id")
	}

	return session, nil
}

func DeleteSession(kubeConfigPath string, sessionID string) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return nil
	}

	client, err := kube.NewClient(kubeConfigPath)
	if err != nil {
		return fmt.Errorf("create kubernetes client: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(context.Background(), managerRequestTimeout)
	defer cancel()

	err = client.Clientset.CoreV1().RESTClient().Delete().
		Namespace(defaultManagerNamespace).
		Resource("services").
		Name(serviceProxyName()).
		SubResource("proxy").
		Suffix("v1", "sessions", trimmedSessionID).
		Do(requestCtx).
		Error()
	if err == nil || k8serrors.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("delete manager session %q: %w", trimmedSessionID, err)
}

func ListSessions(kubeConfigPath string) ([]contracts.DebugSession, error) {
	client, err := kube.NewClient(kubeConfigPath)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(context.Background(), managerRequestTimeout)
	defer cancel()

	responseBody, err := client.Clientset.CoreV1().RESTClient().Get().
		Namespace(defaultManagerNamespace).
		Resource("services").
		Name(serviceProxyName()).
		SubResource("proxy").
		Suffix("v1", "sessions").
		Do(requestCtx).
		Raw()
	if err != nil {
		return nil, fmt.Errorf("list manager sessions: %w", err)
	}

	var response contracts.ListDebugSessionsResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, fmt.Errorf("decode manager sessions response: %w", err)
	}
	return response.Sessions, nil
}

func serviceProxyName() string {
	return fmt.Sprintf("http:%s:%d", defaultManagerServiceName, defaultManagerServicePort)
}

func NormalizeNamespace(namespace string) string {
	trimmed := strings.TrimSpace(namespace)
	if trimmed == "" {
		return "default"
	}
	return trimmed
}
