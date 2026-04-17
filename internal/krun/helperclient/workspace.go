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

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun/helper"
)

var errHelperStreamDone = errors.New("helper stream done")

func WorkspaceList(config cfg.Config) (contracts.ServiceListResponse, error) {
	if err := helper.EnsureStarted(config); err != nil {
		return contracts.ServiceListResponse{}, fmt.Errorf("helper unreachable: %w", err)
	}

	request, err := http.NewRequest(http.MethodGet, helper.BaseURL+"/v1/services", nil)
	if err != nil {
		return contracts.ServiceListResponse{}, err
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return contracts.ServiceListResponse{}, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return contracts.ServiceListResponse{}, err
	}
	if response.StatusCode >= http.StatusBadRequest {
		return contracts.ServiceListResponse{}, parseHelperError(response.StatusCode, body)
	}

	var list contracts.ServiceListResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &list); err != nil {
			return contracts.ServiceListResponse{}, fmt.Errorf("invalid helper response: %w", err)
		}
	}
	return list, nil
}

func WorkspaceBuild(config cfg.Config, request contracts.BuildRequest, out io.Writer) error {
	return workspaceStreamRequest(config, "/v1/build", request, out)
}

func WorkspaceDeploy(config cfg.Config, request contracts.DeployRequest, out io.Writer) error {
	return workspaceStreamRequest(config, "/v1/deploy", request, out)
}

func WorkspaceDelete(config cfg.Config, request contracts.DeleteRequest, out io.Writer) error {
	return workspaceStreamRequest(config, "/v1/delete", request, out)
}

func workspaceStreamRequest(config cfg.Config, path string, payload any, out io.Writer) error {
	if err := helper.EnsureStarted(config); err != nil {
		return fmt.Errorf("helper unreachable: %w", err)
	}
	if out == nil {
		out = io.Discard
	}

	marshaledBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	request, err := http.NewRequest(http.MethodPost, helper.BaseURL+path, bytes.NewReader(marshaledBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")

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
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

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
