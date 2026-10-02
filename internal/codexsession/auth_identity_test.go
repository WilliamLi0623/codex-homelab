package codexsession

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func identityFixture(t *testing.T, accountID, userID string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    userID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture-signature"
	auth, err := json.Marshal(map[string]any{"tokens": map[string]string{"id_token": jwt}})
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func TestCodexAuthIdentityMatchesStableAccountAndUserClaims(t *testing.T) {
	expected := identityFixture(t, "workspace-a", "user-a")
	matching := identityFixture(t, "workspace-a", "user-a")
	differentWorkspace := identityFixture(t, "workspace-b", "user-a")
	differentUser := identityFixture(t, "workspace-a", "user-b")

	for _, test := range []struct {
		name string
		got  []byte
		want bool
	}{
		{name: "matching", got: matching, want: true},
		{name: "different workspace", got: differentWorkspace, want: false},
		{name: "different user", got: differentUser, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := CodexAuthIdentityMatches(expected, test.got)
			if err != nil {
				t.Fatalf("CodexAuthIdentityMatches() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("CodexAuthIdentityMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCodexAuthIdentityRejectsMissingOrMalformedClaimsWithoutLeakingInput(t *testing.T) {
	valid := identityFixture(t, "private-workspace", "private-user")
	malformed := []byte(`{"tokens":{"id_token":"private-access-token"}}`)
	missingClaims := identityFixture(t, "", "")

	for _, test := range []struct {
		name  string
		left  []byte
		right []byte
	}{
		{name: "malformed candidate", left: valid, right: malformed},
		{name: "missing stable claims", left: missingClaims, right: valid},
		{name: "missing baseline", left: nil, right: valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := CodexAuthIdentityMatches(test.left, test.right)
			if err == nil {
				t.Fatal("CodexAuthIdentityMatches() accepted incomplete identity data")
			}
			if got := err.Error(); got == "" || got == "private-access-token" || got == "private-workspace" || got == "private-user" {
				t.Fatalf("unexpected error text may expose input: %q", got)
			}
		})
	}
}
