package llm

import (
	"testing"

	"agent-platform-runner-go/internal/api"
)

func mustEncodeHITLSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestNormalizeHITLApprovalSubmitSupportsApproveRuleRunForCommand(t *testing.T) {
	normalized, err := normalizeHITLApprovalSubmit(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "chmod 777 ~/a.sh"},
		},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": "approve_rule_run", "reason": "同规则本轮一并放行"},
	}))
	if err != nil {
		t.Fatalf("normalizeHITLApprovalSubmit returned error: %v", err)
	}

	approvals, ok := normalized["approvals"].([]map[string]any)
	if !ok || len(approvals) != 1 {
		t.Fatalf("expected one normalized approval, got %#v", normalized)
	}
	if normalized["status"] != "answered" {
		t.Fatalf("expected answered status, got %#v", normalized)
	}
	if approvals[0]["decision"] != "approve_rule_run" {
		t.Fatalf("expected approve_rule_run decision to be preserved, got %#v", approvals[0])
	}
	if approvals[0]["reason"] != "同规则本轮一并放行" {
		t.Fatalf("expected reason to be preserved, got %#v", approvals[0])
	}
}

func TestNormalizeHITLApprovalSubmitSupportsApproveRuleRunForFile(t *testing.T) {
	normalized, err := normalizeHITLApprovalSubmit(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "read /tmp/owner.md"},
		},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": "approve_rule_run", "reason": "同规则本轮一并放行"},
	}))
	if err != nil {
		t.Fatalf("normalizeHITLApprovalSubmit returned error: %v", err)
	}

	approvals, ok := normalized["approvals"].([]map[string]any)
	if !ok || len(approvals) != 1 {
		t.Fatalf("expected one normalized approval, got %#v", normalized)
	}
	if approvals[0]["decision"] != "approve_rule_run" {
		t.Fatalf("expected approve_rule_run decision to be preserved, got %#v", approvals[0])
	}
	if approvals[0]["reason"] != "同规则本轮一并放行" {
		t.Fatalf("expected reason to be preserved, got %#v", approvals[0])
	}
}

func TestNormalizeHITLApprovalSubmitRejectsEmptyDecision(t *testing.T) {
	_, err := normalizeHITLApprovalSubmit(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "chmod 777 ~/a.sh"},
		},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": ""},
	}))
	if err == nil || err.Error() != "items[0]: decision is required" {
		t.Fatalf("expected empty decision error, got %v", err)
	}
}

func TestNormalizeHITLApprovalSubmitRejectsUnknownDecision(t *testing.T) {
	_, err := normalizeHITLApprovalSubmit(map[string]any{
		"approvals": []any{
			map[string]any{"id": "tool_1", "command": "chmod 777 ~/a.sh"},
		},
	}, mustEncodeHITLSubmitParams(t, []map[string]any{
		{"id": "tool_1", "decision": "approve_always", "reason": "历史回放"},
	}))
	if err == nil || err.Error() != `items[0]: unsupported approval decision "approve_always"` {
		t.Fatalf("expected unsupported decision error, got %v", err)
	}
}
