package helperclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/user"
	"strings"

	cfg "github.com/ftechmax/krun/internal/config"
	"github.com/ftechmax/krun/internal/contracts"
	"github.com/ftechmax/krun/internal/krun/helper"
)

var lookupCurrentUser = user.Current

func HelperDebugSessionsList(config cfg.Config) ([]contracts.HelperDebugSession, error) {
	if err := helper.EnsureStarted(config); err != nil {
		return nil, fmt.Errorf("helper unreachable: %w", err)
	}

	var response contracts.HelperDebugSessionsResponse
	if err := helperJSONRequest(http.MethodGet, "/v1/debug/sessions", nil, &response); err != nil {
		return nil, err
	}
	return response.Sessions, nil
}

func HelperDebugEnable(config cfg.Config, context contracts.DebugServiceContext, containerName string) (contracts.HelperResponse, error) {
	if err := helper.EnsureStarted(config); err != nil {
		return contracts.HelperResponse{}, fmt.Errorf("helper unreachable: %w", err)
	}

	request := contracts.DebugSessionCommandRequest{
		Context:       context,
		ContainerName: containerName,
		User:          resolveDebugSessionUser(),
	}
	var response contracts.HelperResponse
	err := helperJSONRequest(http.MethodPost, "/v1/debug/enable", request, &response)
	return response, err
}

func HelperDebugDisable(config cfg.Config, context contracts.DebugServiceContext) (contracts.HelperResponse, error) {
	if err := helper.EnsureStarted(config); err != nil {
		return contracts.HelperResponse{}, fmt.Errorf("helper unreachable: %w", err)
	}

	request := contracts.DebugSessionCommandRequest{Context: context, User: resolveDebugSessionUser()}
	var response contracts.HelperResponse
	err := helperJSONRequest(http.MethodPost, "/v1/debug/disable", request, &response)
	return response, err
}

func helperJSONRequest(method string, path string, payload any, responseTarget any) error {
	var body io.Reader
	if payload != nil {
		marshaled, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(marshaled)
	}

	request, err := http.NewRequest(method, helper.BaseURL+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

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

func resolveDebugSessionUser() contracts.DebugSessionUser {
	record, err := lookupCurrentUser()
	if err != nil {
		return contracts.DebugSessionUser{}
	}

	return contracts.DebugSessionUser{
		UID: strings.TrimSpace(record.Uid),
		GID: strings.TrimSpace(record.Gid),
	}
}
