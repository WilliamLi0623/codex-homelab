package codexrouting

import (
	"testing"
)

func TestClassifyQuota(t *testing.T) {
	allowed := func(value bool) *bool { return &value }
	percent := func(value int) *int { return &value }
	tests := []struct {
		name     string
		snapshot QuotaSnapshot
		want     Mode
	}{
		{name: "explicit ordinary usage allowed", snapshot: QuotaSnapshot{OrdinaryUsageAllowed: allowed(true)}, want: ModeNormal},
		{name: "explicit ordinary usage denied", snapshot: QuotaSnapshot{OrdinaryUsageAllowed: allowed(false)}, want: ModeQuotaFallback},
		{name: "recognized rate limit reached", snapshot: QuotaSnapshot{RateLimitReachedType: "rate_limit_reached"}, want: ModeQuotaFallback},
		{name: "workspace credits depleted", snapshot: QuotaSnapshot{RateLimitReachedType: "workspace_owner_credits_depleted"}, want: ModeQuotaFallback},
		{name: "reached type takes precedence", snapshot: QuotaSnapshot{OrdinaryUsageAllowed: allowed(true), RateLimitReachedType: "workspace_member_usage_limit_reached"}, want: ModeQuotaFallback},
		{name: "missing fields", snapshot: QuotaSnapshot{}, want: ModeUnknown},
		{name: "unavailable ordinary usage", snapshot: QuotaSnapshot{OrdinaryUsageAllowed: nil}, want: ModeUnknown},
		{name: "unknown reached type", snapshot: QuotaSnapshot{RateLimitReachedType: "provider_unavailable"}, want: ModeUnknown},
		{name: "percentage and reset data are not a signal", snapshot: QuotaSnapshot{PrimaryUsedPercent: percent(100), PrimaryResetUnix: 42}, want: ModeUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyQuota(tt.snapshot); got != tt.want {
				t.Fatalf("ClassifyQuota(%+v) = %q, want %q", tt.snapshot, got, tt.want)
			}
		})
	}
}
