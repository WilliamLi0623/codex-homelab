package codexrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HTTPModeSink publishes only the routing-state document. The bearer token is
// held in memory for requests and is never included in errors or responses.
type HTTPModeSink struct {
	URL    string
	Token  string
	Client *http.Client
}

// NewHTTPModeSink creates a sink for a controller base URL. The token remains
// process-local and is never persisted by this package.
func NewHTTPModeSink(controllerBaseURL, token string) HTTPModeSink {
	return HTTPModeSink{
		URL:   strings.TrimRight(controllerBaseURL, "/") + "/internal/v1/routing-state",
		Token: token,
	}
}

func (s HTTPModeSink) Publish(ctx context.Context, state RoutingState) error {
	if err := validateRoutingState(state); err != nil {
		return err
	}
	if strings.TrimSpace(s.URL) == "" {
		return errors.New("routing-state sink URL is required")
	}
	if s.Token == "" {
		return errors.New("routing-state sink token is required")
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	// Keep the bound even when an injected client has no Client.Timeout.
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	payload := struct {
		Mode       Mode   `json:"mode"`
		ObservedAt string `json:"observed_at"`
		Generation int64  `json:"generation"`
	}{state.Mode, state.ObservedAt.UTC().Format(time.RFC3339Nano), state.Generation}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode routing state: %w", err)
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create routing-state request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("publish routing state: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("publish routing state: controller returned HTTP %d", resp.StatusCode)
	}
	return nil
}
