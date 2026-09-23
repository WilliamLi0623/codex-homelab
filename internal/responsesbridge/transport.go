package responsesbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Upstream interface {
	Do(ctx context.Context, request ChatRequest) (*http.Response, error)
}

type HTTPUpstream struct {
	URL     string
	APIKey  string
	Client  *http.Client
	Timeout time.Duration
}

func (u HTTPUpstream) Do(ctx context.Context, request ChatRequest) (*http.Response, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Chat request: %w", err)
	}
	client := u.Client
	if client == nil {
		client = &http.Client{}
	}
	if u.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, u.Timeout)
		defer cancel()
	}
	url := strings.TrimRight(u.URL, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if u.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+u.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &UpstreamError{Status: http.StatusGatewayTimeout, Class: "timeout"}
		}
		return nil, &UpstreamError{Status: http.StatusBadGateway, Class: "network"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		class := "provider_failure"
		switch resp.StatusCode {
		case 400:
			class = "invalid_request"
		case 401, 403:
			class = "authentication"
		case 429:
			class = "rate_limit"
		}
		return nil, &UpstreamError{Status: resp.StatusCode, Class: class}
	}
	return resp, nil
}
