package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func TestPlanningWriteCreatesMarkdownFile(t *testing.T) {
	root := t.TempDir()
	executor := &RuntimeToolExecutor{cfg: config.Config{Paths: config.PathsConfig{ChatsDir: root}}}
	execCtx := &ExecutionContext{
		Request: api.QueryRequest{Message: "改造 CODER planningMode"},
		Session: QuerySession{
			RequestID:    "req_1",
			RunID:        "run_123",
			ChatID:       "chat_1",
			AgentKey:     "coder",
			PlanningMode: true,
		},
	}

	result, err := executor.Invoke(context.Background(), "planning_write", map[string]any{
		"title":       "改造 CODER planningMode",
		"summary":     "Write a standard planning document.",
		"keyChanges":  []any{"Add planning_write"},
		"steps":       []any{"Write the markdown file"},
		"testPlan":    []any{"Run go test"},
		"assumptions": []any{"Use CHATS_DIR/plans"},
	}, execCtx)
	if err != nil {
		t.Fatalf("invoke planning_write: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected success, got %#v", result)
	}
	planningID := AnyStringNode(result.Structured["planningId"])
	if planningID != "改造-CODER-planningMode-run_123" {
		t.Fatalf("unexpected planningId %q", planningID)
	}
	planningFile := AnyStringNode(result.Structured["planningFile"])
	if planningFile != filepath.Join(root, "plans", planningID+".md") {
		t.Fatalf("unexpected planningFile %q", planningFile)
	}
	data, readErr := os.ReadFile(planningFile)
	if readErr != nil {
		t.Fatalf("read planning file: %v", readErr)
	}
	text := string(data)
	for _, want := range []string{"# 改造 CODER planningMode", "## Summary", "## Key Changes", "## Plan", "## Test Plan", "## Assumptions"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected markdown to contain %q, got:\n%s", want, text)
		}
	}
	if execCtx.PlanningState == nil || execCtx.PlanningState.PlanningID != planningID {
		t.Fatalf("expected execution context planning state, got %#v", execCtx.PlanningState)
	}
}
