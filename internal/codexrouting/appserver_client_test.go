package codexrouting

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeRateLimitsTransport struct {
	payload json.RawMessage
	err     error
	method  string
	params  any
}

func (f *fakeRateLimitsTransport) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	f.method = method
	f.params = params
	return f.payload, f.err
}

func rateLimitsPayload(ordinary any, reached any) json.RawMessage {
	value := map[string]any{
		"rateLimits": map[string]any{"rateLimitReachedType": reached},
	}
	if ordinary != nil {
		value["ordinaryUsageAllowed"] = ordinary
	}
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func TestAppServerQuotaClientReadsAuthoritativeSignals(t *testing.T) {
	tests := []struct {
		name     string
		payload  json.RawMessage
		wantMode Mode
	}{
		{name: "ordinary available", payload: rateLimitsPayload(true, nil), wantMode: ModeNormal},
		{name: "ordinary denied", payload: rateLimitsPayload(false, nil), wantMode: ModeQuotaFallback},
		{name: "recognized reached", payload: rateLimitsPayload(true, "rate_limit_reached"), wantMode: ModeQuotaFallback},
		{name: "workspace credits depleted", payload: rateLimitsPayload(nil, "workspace_member_credits_depleted"), wantMode: ModeQuotaFallback},
		{name: "ordinary null", payload: []byte(`{"ordinaryUsageAllowed":null,"rateLimits":{}}`), wantMode: ModeUnknown},
		{name: "unrecognized reached", payload: rateLimitsPayload(true, "provider_unavailable"), wantMode: ModeUnknown},
		{name: "missing ordinary", payload: []byte(`{"rateLimits":{}}`), wantMode: ModeUnknown},
		{name: "percentages do not decide", payload: []byte(`{"ordinaryUsageAllowed":null,"rateLimits":{"primary":{"usedPercent":100,"resetsAt":1}}}`), wantMode: ModeUnknown},
		{name: "percentages do not decide without a reached type", payload: []byte(`{"ordinaryUsageAllowed":null,"rateLimitsByLimitId":{"account-identity":{"limitId":"private","primary":{"usedPercent":100,"resetsAt":123},"secondary":{"usedPercent":100,"resetsAt":null}}}}`), wantMode: ModeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &fakeRateLimitsTransport{payload: tt.payload}
			snapshot, err := (AppServerQuotaClient{Transport: transport}).Read(context.Background())
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if got := ClassifyQuota(snapshot); got != tt.wantMode {
				t.Fatalf("ClassifyQuota(%+v) = %q, want %q", snapshot, got, tt.wantMode)
			}
			if transport.method != accountRateLimitsReadMethod {
				t.Fatalf("method = %q, want %q", transport.method, accountRateLimitsReadMethod)
			}
		})
	}
}

func TestAppServerQuotaClientDecodesLimitMapDiagnosticsAndDiscardsIdentity(t *testing.T) {
	payload := []byte(`{
		"ordinaryUsageAllowed":true,
		"accountId":"account-secret",
		"rateLimitsByLimitId":{
			"account-key-that-must-not-be-retained":{
				"accountId":"account-secret",
				"limitId":"private-limit",
				"rateLimitReachedType":null,
				"primary":{"usedPercent":37,"resetsAt":1700000000},
				"secondary":{"usedPercent":83,"resetsAt":null}
			}
		}
	}`)

	snapshot, err := (AppServerQuotaClient{Transport: &fakeRateLimitsTransport{payload: payload}}).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if snapshot.PrimaryUsedPercent == nil || *snapshot.PrimaryUsedPercent != 37 {
		t.Fatalf("PrimaryUsedPercent = %v, want 37", snapshot.PrimaryUsedPercent)
	}
	if snapshot.SecondaryUsedPercent == nil || *snapshot.SecondaryUsedPercent != 83 {
		t.Fatalf("SecondaryUsedPercent = %v, want 83", snapshot.SecondaryUsedPercent)
	}
	if snapshot.PrimaryResetUnix != 1700000000 {
		t.Fatalf("PrimaryResetUnix = %d, want 1700000000", snapshot.PrimaryResetUnix)
	}
	if snapshot.SecondaryResetUnix != 0 {
		t.Fatalf("SecondaryResetUnix = %d, want zero for null", snapshot.SecondaryResetUnix)
	}
	if snapshot.RateLimitReachedType != "" {
		t.Fatalf("RateLimitReachedType = %q, want empty", snapshot.RateLimitReachedType)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal(snapshot) error = %v", err)
	}
	for _, identity := range []string{"account-secret", "private-limit", "account-key-that-must-not-be-retained"} {
		if strings.Contains(string(encoded), identity) {
			t.Fatalf("snapshot retained identity %q: %s", identity, encoded)
		}
	}
}

func TestAppServerQuotaClientUsesRecognizedReachedTypeFromLimitMap(t *testing.T) {
	payload := []byte(`{
		"ordinaryUsageAllowed":true,
		"rateLimits":null,
		"rateLimitsByLimitId":{
			"opaque-limit-id":{
				"rateLimitReachedType":"workspace_member_credits_depleted",
				"primary":{"usedPercent":100,"resetsAt":1700000000},
				"secondary":{"usedPercent":0,"resetsAt":null}
			}
		}
	}`)

	snapshot, err := (AppServerQuotaClient{Transport: &fakeRateLimitsTransport{payload: payload}}).Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got := ClassifyQuota(snapshot); got != ModeQuotaFallback {
		t.Fatalf("ClassifyQuota(%+v) = %q, want %q", snapshot, got, ModeQuotaFallback)
	}
}

func TestAppServerQuotaClientAllowsNullOrMissingDiagnostics(t *testing.T) {
	for _, payload := range []json.RawMessage{
		[]byte(`{"ordinaryUsageAllowed":true,"rateLimits":{"primary":null,"secondary":null}}`),
		[]byte(`{"ordinaryUsageAllowed":true,"rateLimits":{"primary":{},"secondary":{}}}`),
		[]byte(`{"ordinaryUsageAllowed":true,"rateLimitsByLimitId":{"opaque":{"primary":null,"secondary":null}}}`),
	} {
		t.Run(string(payload), func(t *testing.T) {
			snapshot, err := (AppServerQuotaClient{Transport: &fakeRateLimitsTransport{payload: payload}}).Read(context.Background())
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if snapshot.PrimaryUsedPercent != nil || snapshot.SecondaryUsedPercent != nil {
				t.Fatalf("snapshot retained missing diagnostics: %+v", snapshot)
			}
			if snapshot.PrimaryResetUnix != 0 || snapshot.SecondaryResetUnix != 0 {
				t.Fatalf("snapshot retained null diagnostics: %+v", snapshot)
			}
		})
	}
}

func TestAppServerQuotaClientRecognizesEveryReachedType(t *testing.T) {
	for _, reached := range []string{
		"rate_limit_reached",
		"workspace_owner_credits_depleted",
		"workspace_member_credits_depleted",
		"workspace_owner_usage_limit_reached",
		"workspace_member_usage_limit_reached",
	} {
		t.Run(reached, func(t *testing.T) {
			snapshot, err := (AppServerQuotaClient{Transport: &fakeRateLimitsTransport{
				payload: rateLimitsPayload(true, reached),
			}}).Read(context.Background())
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			if got := ClassifyQuota(snapshot); got != ModeQuotaFallback {
				t.Fatalf("ClassifyQuota(%+v) = %q, want %q", snapshot, got, ModeQuotaFallback)
			}
		})
	}
}

func TestAppServerQuotaClientDoesNotTreatTransportStatusAsQuota(t *testing.T) {
	for _, status := range []string{"HTTP 429 rate limited", "HTTP 503 unavailable", "network timeout", "unauthorized"} {
		t.Run(status, func(t *testing.T) {
			transportErr := errors.New(status)
			_, err := (AppServerQuotaClient{Transport: &fakeRateLimitsTransport{err: transportErr}}).Read(context.Background())
			if err == nil || !IsReadErrorKind(err, ReadErrorTransport) {
				t.Fatalf("Read() error = %v, want transport error", err)
			}
			if ClassifyQuota(QuotaSnapshot{}) == ModeQuotaFallback {
				t.Fatal("transport failure must not be classified as quota exhausted")
			}
		})
	}
}

func TestAppServerQuotaClientRejectsMalformedProtocol(t *testing.T) {
	for _, payload := range []json.RawMessage{
		[]byte(``),
		[]byte(`{`),
		[]byte(`{"ordinaryUsageAllowed":"yes","rateLimits":{}}`),
		[]byte(`{"ordinaryUsageAllowed":true}`),
		[]byte(`{"ordinaryUsageAllowed":true,"rateLimits":{"rateLimitReachedType":42}}`),
		[]byte(`{"rateLimits":{}} trailing`),
		[]byte(`{"rateLimits":{}} {"rateLimits":{}}`),
	} {
		_, err := (AppServerQuotaClient{Transport: &fakeRateLimitsTransport{payload: payload}}).Read(context.Background())
		if err == nil || !IsReadErrorKind(err, ReadErrorProtocol) {
			t.Fatalf("payload %q: error = %v, want protocol error", payload, err)
		}
	}
}

func TestAppServerQuotaClientNilTransportIsTyped(t *testing.T) {
	_, err := (AppServerQuotaClient{}).Read(context.Background())
	if err == nil || !IsReadErrorKind(err, ReadErrorTransport) {
		t.Fatalf("error = %v, want typed transport error", err)
	}
}
