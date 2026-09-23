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
	URL       string
	APIKey    string
	UserAgent string
	Client    *http.Client
	Timeout   time.Duration
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
	requestCtx := ctx
	var cancel context.CancelFunc
	if u.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, u.Timeout)
	}
	url := strings.TrimRight(u.URL, "/")
	if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("create upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if u.UserAgent != "" {
		req.Header.Set("User-Agent", u.UserAgent)
	}
	if u.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+u.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		if errors.Is(requestCtx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return nil, &UpstreamError{Status: 499, Class: "client_cancelled"}
		}
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, &UpstreamError{Status: http.StatusGatewayTimeout, Class: "timeout"}
		}
		return nil, &UpstreamError{Status: http.StatusBadGateway, Class: "network"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if cancel != nil {
			cancel()
		}
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
	if cancel != nil {
		resp.Body = &cancelOnCloseReadCloser{ReadCloser: resp.Body, cancel: cancel}
	}
	return resp, nil
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseReadCloser) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
