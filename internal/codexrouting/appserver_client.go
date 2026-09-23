package codexrouting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const accountRateLimitsReadMethod = "account/rateLimits/read"

// RateLimitsTransport sends one App Server RPC and returns the decoded result
// payload. Implementations must not retain or log request data or credentials.
type RateLimitsTransport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// ReadErrorKind identifies whether a snapshot read failed in transport or
// protocol handling. Neither kind is evidence that quota has been exhausted.
type ReadErrorKind string

const (
	ReadErrorTransport ReadErrorKind = "transport"
	ReadErrorProtocol  ReadErrorKind = "protocol"
)

// ReadError is returned for a failed read. Callers should preserve the
// existing routing mode when one is returned.
type ReadError struct {
	Kind ReadErrorKind
	Err  error
}

func (e *ReadError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("codex app server rate-limits %s error: %v", e.Kind, e.Err)
}

func (e *ReadError) Unwrap() error { return e.Err }

// IsReadErrorKind reports whether err is a read error of the requested kind.
func IsReadErrorKind(err error, kind ReadErrorKind) bool {
	var readErr *ReadError
	return errors.As(err, &readErr) && readErr.Kind == kind
}

// AppServerQuotaClient reads the authoritative account quota snapshot without
// launching, restarting, or configuring a Codex process.
type AppServerQuotaClient struct {
	Transport RateLimitsTransport
}

// Read performs exactly one account/rateLimits/read RPC. A successful read
// can still classify as ModeUnknown when authoritative fields are absent,
// null, or unrecognized.
func (c AppServerQuotaClient) Read(ctx context.Context) (QuotaSnapshot, error) {
	if c.Transport == nil {
		return QuotaSnapshot{}, &ReadError{Kind: ReadErrorTransport, Err: errors.New("nil rate-limits transport")}
	}
	payload, err := c.Transport.Call(ctx, accountRateLimitsReadMethod, struct{}{})
	if err != nil {
		return QuotaSnapshot{}, &ReadError{Kind: ReadErrorTransport, Err: err}
	}

	snapshot, err := parseRateLimitsResult(payload)
	if err != nil {
		return QuotaSnapshot{}, &ReadError{Kind: ReadErrorProtocol, Err: err}
	}
	return snapshot, nil
}

type rateLimitsResult struct {
	OrdinaryUsageAllowed *bool           `json:"ordinaryUsageAllowed"`
	RateLimits           json.RawMessage `json:"rateLimits"`
	RateLimitsByLimitID  json.RawMessage `json:"rateLimitsByLimitId"`
}

type rateLimitWindow struct {
	UsedPercent *int   `json:"usedPercent"`
	ResetsAt    *int64 `json:"resetsAt"`
}

type rateLimitsData struct {
	RateLimitReachedType *string          `json:"rateLimitReachedType"`
	Primary              *rateLimitWindow `json:"primary"`
	Secondary            *rateLimitWindow `json:"secondary"`
}

func parseRateLimitsResult(payload json.RawMessage) (QuotaSnapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var result rateLimitsResult
	if err := decoder.Decode(&result); err != nil {
		return QuotaSnapshot{}, fmt.Errorf("decode result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return QuotaSnapshot{}, errors.New("decode result: trailing JSON")
	}
	legacyPresent := jsonObjectPresent(result.RateLimits)
	mapPresent := jsonObjectPresent(result.RateLimitsByLimitID)
	if !legacyPresent && !mapPresent {
		return QuotaSnapshot{}, errors.New("decode result: missing rateLimits or rateLimitsByLimitId object")
	}

	var legacy rateLimitsData
	if legacyPresent {
		if err := json.Unmarshal(result.RateLimits, &legacy); err != nil {
			return QuotaSnapshot{}, fmt.Errorf("decode rateLimits: %w", err)
		}
	}

	snapshot := QuotaSnapshot{OrdinaryUsageAllowed: result.OrdinaryUsageAllowed}
	if legacyPresent {
		applyRateLimitDiagnostics(&snapshot, legacy)
	}

	if mapPresent {
		byLimitID, err := decodeRateLimitsByLimitID(result.RateLimitsByLimitID)
		if err != nil {
			return QuotaSnapshot{}, err
		}
		applyRateLimitReachedType(&snapshot, legacy, byLimitID)
		if !legacyPresent && len(byLimitID) == 1 {
			for _, limits := range byLimitID {
				applyRateLimitDiagnostics(&snapshot, limits)
			}
		}
	}
	return snapshot, nil
}

func jsonObjectPresent(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func decodeRateLimitsByLimitID(raw json.RawMessage) (map[string]rateLimitsData, error) {
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, fmt.Errorf("decode rateLimitsByLimitId: %w", err)
	}
	decoded := make(map[string]rateLimitsData, len(encoded))
	for _, rawSnapshot := range encoded {
		if len(rawSnapshot) == 0 || string(rawSnapshot) == "null" {
			return nil, errors.New("decode rateLimitsByLimitId: null snapshot")
		}
		var limits rateLimitsData
		if err := json.Unmarshal(rawSnapshot, &limits); err != nil {
			return nil, fmt.Errorf("decode rateLimitsByLimitId snapshot: %w", err)
		}
		decoded[fmt.Sprintf("entry-%d", len(decoded))] = limits
	}
	return decoded, nil
}

func applyRateLimitDiagnostics(snapshot *QuotaSnapshot, limits rateLimitsData) {
	if limits.RateLimitReachedType != nil {
		snapshot.RateLimitReachedType = *limits.RateLimitReachedType
	}
	if limits.Primary != nil {
		snapshot.PrimaryUsedPercent = limits.Primary.UsedPercent
		if limits.Primary.ResetsAt != nil {
			snapshot.PrimaryResetUnix = *limits.Primary.ResetsAt
		}
	}
	if limits.Secondary != nil {
		snapshot.SecondaryUsedPercent = limits.Secondary.UsedPercent
		if limits.Secondary.ResetsAt != nil {
			snapshot.SecondaryResetUnix = *limits.Secondary.ResetsAt
		}
	}
}

func applyRateLimitReachedType(snapshot *QuotaSnapshot, legacy rateLimitsData, byLimitID map[string]rateLimitsData) {
	if legacy.RateLimitReachedType != nil {
		return
	}
	var candidate string
	for _, limits := range byLimitID {
		if limits.RateLimitReachedType == nil {
			continue
		}
		if candidate == "" {
			candidate = *limits.RateLimitReachedType
			continue
		}
		if candidate != *limits.RateLimitReachedType {
			return
		}
	}
	snapshot.RateLimitReachedType = candidate
}
