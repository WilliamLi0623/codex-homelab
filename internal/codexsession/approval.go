package codexsession

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrUnsupportedApproval = errors.New("unsupported App Server approval request")

const (
	maxApprovalCommandBytes = 16 << 10
	maxApprovalPathBytes    = 4 << 10
	maxApprovalReasonBytes  = 4 << 10
)

// ApprovalRequest contains only the fields needed to render an approval. It
// intentionally omits arbitrary App Server payloads and permission profiles.
type ApprovalRequest struct {
	RequestID json.RawMessage `json:"-"`
	Method    string          `json:"method"`
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId"`
	ItemID    string          `json:"itemId"`
	Kind      string          `json:"kind,omitempty"`
	Command   string          `json:"command,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Decisions []string        `json:"decisions"`
}

func ParseApprovalRequest(request ServerRequest) (ApprovalRequest, error) {
	approval := ApprovalRequest{
		RequestID: append(json.RawMessage(nil), request.ID...),
		Method:    request.Method,
	}
	var common struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(request.Params, &common); err != nil {
		return ApprovalRequest{}, fmt.Errorf("decode App Server approval metadata: %w", err)
	}
	approval.ThreadID, approval.TurnID, approval.ItemID, approval.Reason = common.ThreadID, common.TurnID, common.ItemID, common.Reason
	if common.ThreadID == "" || common.TurnID == "" || common.ItemID == "" || len(common.Reason) > maxApprovalReasonBytes {
		return approval, ErrUnsupportedApproval
	}
	switch request.Method {
	case "item/commandExecution/requestApproval":
		var params struct {
			Command            *string           `json:"command"`
			CWD                *string           `json:"cwd"`
			Kind               string            `json:"kind"`
			AvailableDecisions []json.RawMessage `json:"availableDecisions"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return ApprovalRequest{}, fmt.Errorf("decode command approval: %w", err)
		}
		approval.Kind = params.Kind
		if params.Command != nil {
			approval.Command = *params.Command
		}
		if params.CWD != nil {
			approval.CWD = *params.CWD
		}
		if len(approval.Command) > maxApprovalCommandBytes || len(approval.CWD) > maxApprovalPathBytes {
			return approval, ErrUnsupportedApproval
		}
		for _, raw := range params.AvailableDecisions {
			var decision string
			if json.Unmarshal(raw, &decision) == nil && isEphemeralDecision(decision) {
				approval.Decisions = append(approval.Decisions, decision)
			}
		}
		if len(approval.Decisions) == 0 {
			return approval, ErrUnsupportedApproval
		}
	case "item/fileChange/requestApproval":
		approval.Decisions = []string{"accept", "decline", "cancel"}
	case "item/permissions/requestApproval":
		return approval, ErrUnsupportedApproval
	default:
		return approval, ErrUnsupportedApproval
	}
	return approval, nil
}

func BuildApprovalResponse(method string, params json.RawMessage, decision string) (any, error) {
	if !isEphemeralDecision(decision) {
		return nil, errors.New("approval decision is unsupported or persists beyond this request")
	}
	allowed := false
	switch method {
	case "item/commandExecution/requestApproval":
		var request struct {
			AvailableDecisions []json.RawMessage `json:"availableDecisions"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, fmt.Errorf("decode command approval decisions: %w", err)
		}
		for _, raw := range request.AvailableDecisions {
			var available string
			if json.Unmarshal(raw, &available) == nil && available == decision {
				allowed = true
				break
			}
		}
	case "item/fileChange/requestApproval":
		allowed = true
	default:
		return nil, ErrUnsupportedApproval
	}
	if !allowed {
		return nil, errors.New("approval decision was not offered by App Server")
	}
	return map[string]string{"decision": decision}, nil
}

func isEphemeralDecision(decision string) bool {
	switch decision {
	case "accept", "decline", "cancel":
		return true
	default:
		return false
	}
}
