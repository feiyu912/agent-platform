package server

import (
	"strings"
	"testing"
)

func TestValidateDeferredSubmitParamsAcceptsDismissAndValidShapes(t *testing.T) {
	tests := []struct {
		name   string
		mode   string
		params any
	}{
		{name: "question dismiss", mode: "question", params: []map[string]any{}},
		{name: "question answer", mode: "question", params: []map[string]any{{"answer": "Approve"}}},
		{name: "approval decision", mode: "approval", params: []map[string]any{{"decision": "approve"}}},
		{name: "form approve", mode: "form", params: []map[string]any{{"decision": "approve", "form": map[string]any{"days": 2}}}},
		{name: "form reject", mode: "form", params: []map[string]any{{"decision": "reject"}}},
		{name: "form reject with reason", mode: "form", params: []map[string]any{{"decision": "reject", "reason": "不同意"}}},
		{name: "form reject with form", mode: "form", params: []map[string]any{{"decision": "reject", "reason": "已修改", "form": map[string]any{"days": 1}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams(tt.mode, mustEncodeSubmitParams(t, tt.params))
			if err != nil {
				t.Fatalf("validateDeferredSubmitParams returned error: %v", err)
			}
		})
	}
}

func TestValidateDeferredSubmitParamsRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name       string
		params     any
		wantSubstr string
	}{
		{
			name:       "missing decision",
			params:     []map[string]any{{"form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items require decision",
		},
		{
			name:       "invalid decision",
			params:     []map[string]any{{"decision": "cancel", "form": map[string]any{"days": 2}}},
			wantSubstr: `items[0]: unsupported form decision "cancel"`,
		},
		{
			name:       "approve missing form",
			params:     []map[string]any{{"decision": "approve"}},
			wantSubstr: "items[0]: approve decision requires form",
		},
		{
			name:       "form not object",
			params:     []map[string]any{{"decision": "approve", "form": "bad"}},
			wantSubstr: "items[0]: form field must be an object",
		},
		{
			name:       "action field rejected",
			params:     []map[string]any{{"action": "submit", "form": map[string]any{"days": 2}}},
			wantSubstr: "items[0]: form items no longer use action, use decision instead",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDeferredSubmitParams("form", mustEncodeSubmitParams(t, tt.params))
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
