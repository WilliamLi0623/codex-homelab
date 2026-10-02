package codexsession

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

const maxCodexAuthJSONBytes = 1 << 20

// CodexAuthIdentityMatches compares the ChatGPT workspace and user claims in
// two Codex auth.json documents without returning the identifiers. Codex
// 0.155.0 decodes these JWT claims without verifying their signatures, so this
// is only a consistency check; callers must also require a controlled login
// flow, a matching live App Server account, and a successful authenticated
// request before treating a Session as ready.
func CodexAuthIdentityMatches(expectedAuth, observedAuth []byte) (bool, error) {
	expectedWorkspace, expectedUser, err := parseCodexAuthIdentity(expectedAuth)
	if err != nil {
		return false, err
	}
	observedWorkspace, observedUser, err := parseCodexAuthIdentity(observedAuth)
	if err != nil {
		return false, err
	}
	workspaceMatches := subtle.ConstantTimeCompare([]byte(expectedWorkspace), []byte(observedWorkspace)) == 1
	userMatches := subtle.ConstantTimeCompare([]byte(expectedUser), []byte(observedUser)) == 1
	return workspaceMatches && userMatches, nil
}

func parseCodexAuthIdentity(authJSON []byte) (string, string, error) {
	if len(authJSON) == 0 || len(authJSON) > maxCodexAuthJSONBytes {
		return "", "", errors.New("Codex auth identity data has invalid size")
	}
	var auth struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(authJSON, &auth); err != nil || auth.Tokens.IDToken == "" {
		return "", "", errors.New("Codex auth identity data is incomplete")
	}
	parts := strings.Split(auth.Tokens.IDToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", errors.New("Codex ID token has invalid format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", errors.New("Codex ID token payload is malformed")
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
			ChatGPTUserID    string `json:"chatgpt_user_id"`
			UserID           string `json:"user_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", errors.New("Codex ID token claims are malformed")
	}
	userID := claims.Auth.ChatGPTUserID
	if userID == "" {
		userID = claims.Auth.UserID
	}
	if !validCodexIdentityClaim(claims.Auth.ChatGPTAccountID) || !validCodexIdentityClaim(userID) {
		return "", "", errors.New("Codex ID token lacks stable account identity claims")
	}
	return claims.Auth.ChatGPTAccountID, userID, nil
}

func validCodexIdentityClaim(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
