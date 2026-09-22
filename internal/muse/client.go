package muse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type HTTPClient struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewHTTPClient(baseURL, apiKey string, httpClient *http.Client) *HTTPClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPClient{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: strings.TrimSpace(apiKey), HTTP: httpClient}
}

func (c *HTTPClient) CreateResponse(ctx context.Context, request Request) (Response, error) {
	if c == nil || strings.TrimSpace(c.BaseURL) == "" {
		return Response{}, errors.New("Muse Responses base URL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return Response{}, errors.New("Muse Responses API key is required")
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("encode Muse Responses request: %w", err)
	}
	endpoint := c.BaseURL + "/v1/responses"
	if strings.HasSuffix(c.BaseURL, "/v1") {
		endpoint = c.BaseURL + "/responses"
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("create Muse Responses request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(httpRequest)
	if err != nil {
		return Response{}, fmt.Errorf("send Muse Responses request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Response{}, fmt.Errorf("read Muse Responses response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Response{}, fmt.Errorf("Muse Responses provider returned status %d", response.StatusCode)
	}
	decoded, err := DecodeResponse(responseBody)
	if err != nil {
		return Response{}, err
	}
	return decoded, nil
}
