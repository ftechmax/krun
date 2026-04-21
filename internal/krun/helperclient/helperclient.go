package helperclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
)

var BaseURL = "http://127.0.0.1:47831"
var errHelperStreamDone = errors.New("helper stream done")

const (
	sseScannerInitialBufferSize = 64 * 1024
	sseScannerMaxBufferSize     = 4 * 1024 * 1024
)

func GetServiceByName(config cfg.Config, serviceName string) (cfg.Service, error) {
	var response cfg.Service
	if err := jsonRequest(config, http.MethodGet, "/v1/workspace/service/"+serviceName, nil, &response); err != nil {
		return cfg.Service{}, err
	}
	return response, nil
}

func List(config cfg.Config) ([]string, []string, error) {
	var response contracts.ListResponse
	if err := jsonRequest(config, http.MethodGet, "/v1/workspace/list", nil, &response); err != nil {
		return []string{}, []string{}, err
	}
	return response.Services, response.Projects, nil
}

func Build(config cfg.Config, request contracts.BuildRequest, out io.Writer) error {
	return streamRequest(config, "/v1/workspace/build", request, out)
}

func Deploy(config cfg.Config, request contracts.DeployRequest, out io.Writer) error {
	return streamRequest(config, "/v1/workspace/deploy", request, out)
}

func Delete(config cfg.Config, request contracts.DeleteRequest, out io.Writer) error {
	return streamRequest(config, "/v1/workspace/delete", request, out)
}

func DebugSessionsList(config cfg.Config) ([]contracts.HelperDebugSession, error) {
	var response []contracts.HelperDebugSession
	if err := jsonRequest(config, http.MethodGet, "/v1/debug/sessions", nil, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func DebugEnable(config cfg.Config, service cfg.Service, containerName string) (contracts.HelperResponse, error) {
	request := contracts.DebugEnableRequest{Context: buildDebugServiceContext(service), ContainerName: containerName, KubeConfig: config.KubeConfig}
	var response contracts.HelperResponse
	err := jsonRequest(config, http.MethodPost, "/v1/debug/enable", request, &response)
	return response, err
}

func DebugDisable(config cfg.Config, service cfg.Service) (contracts.HelperResponse, error) {
	request := contracts.DebugDisableRequest{Context: buildDebugServiceContext(service), KubeConfig: config.KubeConfig}
	var response contracts.HelperResponse
	err := jsonRequest(config, http.MethodPost, "/v1/debug/disable", request, &response)
	return response, err
}

func buildDebugServiceContext(service cfg.Service) contracts.DebugServiceContext {
	dependencies := make([]contracts.DebugServiceDependencyContext, 0, len(service.ServiceDependencies))
	for _, dependency := range service.ServiceDependencies {
		dependencies = append(dependencies, contracts.DebugServiceDependencyContext{
			Host:      dependency.Host,
			Namespace: dependency.Namespace,
			Service:   dependency.Service,
			Port:      dependency.Port,
			Aliases:   dependency.Aliases,
		})
	}

	return contracts.DebugServiceContext{
		Project:             service.Project,
		Path:                service.Path,
		ServiceName:         service.Name,
		Namespace:           service.Namespace,
		ContainerPort:       service.ContainerPort,
		InterceptPort:       service.InterceptPort,
		ServiceDependencies: dependencies,
	}
}

func jsonRequest(config cfg.Config, method string, path string, payload any, responseTarget any) error {
	if err := checkHealth(); err != nil {
		return fmt.Errorf("helper unreachable: %w", err)
	}

	var body io.Reader
	if payload != nil {
		marshaled, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(marshaled)
	}

	request, err := http.NewRequest(method, BaseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	request.Header.Set("Authorization", "Bearer "+config.AuthToken)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode >= http.StatusBadRequest {
		return parseHelperError(response.StatusCode, responseBody)
	}

	if responseTarget != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, responseTarget); err != nil {
			return fmt.Errorf("invalid helper response: %w", err)
		}
	}

	return nil
}

func streamRequest(config cfg.Config, path string, payload any, out io.Writer) error {
	if err := checkHealth(); err != nil {
		return fmt.Errorf("helper unreachable: %w", err)
	}

	if out == nil {
		out = io.Discard
	}

	marshaledBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	request, err := http.NewRequest(http.MethodPost, BaseURL+path, bytes.NewReader(marshaledBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+config.AuthToken)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusBadRequest {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		return parseHelperError(response.StatusCode, body)
	}

	return consumeSSEStream(response.Body, out)
}

func consumeSSEStream(stream io.Reader, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, sseScannerInitialBufferSize), sseScannerMaxBufferSize)

	eventName := ""
	dataLines := make([]string, 0, 2)
	seenDone := false

	for scanner.Scan() {
		line := scanner.Text()
		if shouldDispatchEvent(line, &eventName, &dataLines) {
			err := flushSSEEvent(out, &eventName, &dataLines, &seenDone)
			if errors.Is(err, errHelperStreamDone) {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	err := flushSSEEvent(out, &eventName, &dataLines, &seenDone)
	if errors.Is(err, errHelperStreamDone) {
		return nil
	}
	if err != nil {
		return err
	}
	if !seenDone {
		return fmt.Errorf("helper stream ended without done event")
	}
	return nil
}

func shouldDispatchEvent(line string, eventName *string, dataLines *[]string) bool {
	if line == "" {
		return true
	}
	if strings.HasPrefix(line, ":") {
		return false
	}
	if strings.HasPrefix(line, "event:") {
		*eventName = sseFieldValue(line, "event:")
		return false
	}
	if strings.HasPrefix(line, "data:") {
		*dataLines = append(*dataLines, sseFieldValue(line, "data:"))
	}
	return false
}

func flushSSEEvent(out io.Writer, eventName *string, dataLines *[]string, seenDone *bool) error {
	sawDone, err := dispatchSSEEvent(out, *eventName, *dataLines)
	*eventName = ""
	*dataLines = (*dataLines)[:0]
	if sawDone {
		*seenDone = true
	}
	return err
}

func dispatchSSEEvent(out io.Writer, eventName string, dataLines []string) (bool, error) {
	if eventName == "" && len(dataLines) == 0 {
		return false, nil
	}

	payload := strings.Join(dataLines, "\n")
	switch eventName {
	case "log":
		return false, writeLogEvent(out, payload)
	case "done":
		return true, parseDoneEvent(payload)
	default:
		return false, nil
	}
}

func writeLogEvent(out io.Writer, payload string) error {
	var event contracts.StreamLogEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return fmt.Errorf("decode log event: %w", err)
	}
	if _, err := io.WriteString(out, event.Text); err != nil {
		return err
	}
	return nil
}

func parseDoneEvent(payload string) error {
	var event contracts.StreamDoneEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return fmt.Errorf("decode done event: %w", err)
	}
	if !event.Ok {
		message := strings.TrimSpace(event.Error)
		if message == "" {
			message = "operation failed"
		}
		return errors.New(message)
	}
	return errHelperStreamDone
}

func sseFieldValue(line string, prefix string) string {
	value := strings.TrimPrefix(line, prefix)
	if strings.HasPrefix(value, " ") {
		return value[1:]
	}
	return value
}

func parseHelperError(statusCode int, body []byte) error {
	var helperResponse contracts.HelperResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &helperResponse); err == nil {
			if message := strings.TrimSpace(helperResponse.Message); message != "" {
				return errors.New(message)
			}
		}
	}
	return fmt.Errorf("helper request failed with status %d", statusCode)
}

func checkHealth() error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(BaseURL + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
