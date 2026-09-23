// Package codexrouting contains the host-side, fail-closed quota policy.
package codexrouting

type Mode string

const (
	ModeNormal        Mode = "normal"
	ModeQuotaFallback Mode = "quota_fallback"
	ModeUnknown       Mode = "unknown"
)

// QuotaSnapshot contains only the app-server fields needed for the routing
// decision. Percentage and reset fields are retained for diagnostics only;
// they must never be used to infer exhaustion or recovery.
type QuotaSnapshot struct {
	OrdinaryUsageAllowed *bool
	RateLimitReachedType string
	PrimaryUsedPercent   *int
	SecondaryUsedPercent *int
	PrimaryResetUnix     int64
	SecondaryResetUnix   int64
}

// ClassifyQuota accepts only an explicit, recognized account quota signal.
// Unknown or internally inconsistent observations preserve the current mode.
func ClassifyQuota(snapshot QuotaSnapshot) Mode {
	switch snapshot.RateLimitReachedType {
	case "rate_limit_reached",
		"workspace_owner_credits_depleted",
		"workspace_member_credits_depleted",
		"workspace_owner_usage_limit_reached",
		"workspace_member_usage_limit_reached":
		return ModeQuotaFallback
	case "":
		// Continue to the explicit ordinary-usage field below.
	default:
		return ModeUnknown
	}

	if snapshot.OrdinaryUsageAllowed == nil {
		return ModeUnknown
	}
	if !*snapshot.OrdinaryUsageAllowed {
		return ModeQuotaFallback
	}
	return ModeNormal
}
