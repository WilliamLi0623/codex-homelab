package codexsession

import (
	"encoding/json"
	"testing"
)

func TestBuildApprovalResponseAcceptsOnlyAdvertisedEphemeralCommandDecisions(t *testing.T) {
	params := json.RawMessage(`{"availableDecisions":["accept","decline","cancel"]}`)
	result, err := BuildApprovalResponse("item/commandExecution/requestApproval", params, "accept")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"decision":"accept"}` {
		t.Fatalf("response=%s", encoded)
	}
}

func TestBuildApprovalResponseRejectsUnadvertisedAndSessionWideDecisions(t *testing.T) {
	params := json.RawMessage(`{"availableDecisions":["accept","decline"]}`)
	for _, decision := range []string{"cancel", "acceptForSession"} {
		t.Run(decision, func(t *testing.T) {
			if _, err := BuildApprovalResponse("item/commandExecution/requestApproval", params, decision); err == nil {
				t.Fatalf("decision %q should be rejected", decision)
			}
		})
	}
}

func TestBuildApprovalResponseFailsClosedForPermissionGrantRequests(t *testing.T) {
	if _, err := BuildApprovalResponse("item/permissions/requestApproval", json.RawMessage(`{"additionalPermissions":{"network":{"enabled":true}}}`), "accept"); err == nil {
		t.Fatal("permission grants need a separately designed explicit scope and profile UI")
	}
}

func TestBuildApprovalResponseRejectsUnknownApprovalMethods(t *testing.T) {
	if _, err := BuildApprovalResponse("item/future/requestApproval", json.RawMessage(`{}`), "accept"); err == nil {
		t.Fatal("unknown approval method must fail closed")
	}
}
