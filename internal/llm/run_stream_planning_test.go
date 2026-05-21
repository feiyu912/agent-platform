package llm

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	contracts "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func TestPlanningWriteArgumentsStreamPlanningDeltas(t *testing.T) {
	chatsDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID:    "chat_1",
			RunID:     "run_1",
			RequestID: "req_1",
			AgentKey:  "coder",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatsDir: chatsDir},
			},
		},
	}

	chunks := []string{
		`{"title":"Streaming Plan",`,
		`"summary":"Stream the plan while the tool arguments arrive.",`,
		`"keyChanges":["Emit planning start before completion"],`,
		`"steps":["Parse arguments incrementally","Write the final markdown"],`,
		`"testPlan":["Assert multiple deltas"],`,
		`"assumptions":["The provider emits tool arguments in order"]}`,
	}
	for _, chunk := range chunks {
		stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
			ID:        "tool_plan",
			Name:      "planning_write",
			ArgsDelta: chunk,
		}})
	}

	markdown := planutil.RenderMarkdown(planutil.Spec{
		Title:       "Streaming Plan",
		Summary:     "Stream the plan while the tool arguments arrive.",
		KeyChanges:  []string{"Emit planning start before completion"},
		Steps:       []string{"Parse arguments incrementally", "Write the final markdown"},
		TestPlan:    []string{"Assert multiple deltas"},
		Assumptions: []string{"The provider emits tool arguments in order"},
	})
	stream.appendFinalPlanningDeltas("tool_plan", contracts.ToolExecutionResult{
		Structured: map[string]any{
			"planningId":   "run_1_planning",
			"planningFile": filepath.Join(chatsDir, "plans", "run_1_planning.md"),
			"title":        "Streaming Plan",
			"status":       "ready",
			"markdown":     markdown,
		},
	})

	starts, deltaCount, ends, combined := planningEventStats(stream.pending)
	if starts != 1 {
		t.Fatalf("planning.start count = %d, want 1", starts)
	}
	if deltaCount < 3 {
		t.Fatalf("planning.delta count = %d, want at least 3; events %#v", deltaCount, stream.pending)
	}
	if ends != 1 {
		t.Fatalf("planning.end count = %d, want 1", ends)
	}
	if combined != markdown {
		t.Fatalf("combined planning.delta markdown mismatch\nwant:\n%s\ngot:\n%s", markdown, combined)
	}
}

func TestPlanningWriteCompleteArgumentsSplitIntoMultipleDeltas(t *testing.T) {
	chatsDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID:    "chat_1",
			RunID:     "run_1",
			RequestID: "req_1",
			AgentKey:  "coder",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatsDir: chatsDir},
			},
		},
	}
	args := map[string]any{
		"title":       "One Shot Plan",
		"summary":     "The provider returned full arguments in one chunk.",
		"keyChanges":  []string{"Split rendered markdown by section"},
		"steps":       []string{"Emit several planning delta events"},
		"testPlan":    []string{"Check delta count"},
		"assumptions": []string{"The final tool write succeeds"},
	}
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	stream.appendToolCallDeltas([]contracts.AgentDelta{contracts.DeltaToolCall{
		ID:        "tool_plan",
		Name:      "planning_write",
		ArgsDelta: string(data),
	}})

	_, deltaCount, _, combined := planningEventStats(stream.pending)
	if deltaCount < 3 {
		t.Fatalf("planning.delta count = %d, want multiple section deltas; markdown %q", deltaCount, combined)
	}
	for _, section := range []string{"# One Shot Plan", "## Summary", "## Key Changes", "## Plan", "## Test Plan", "## Assumptions"} {
		if !strings.Contains(combined, section) {
			t.Fatalf("expected combined delta to contain %q, got:\n%s", section, combined)
		}
	}
}

func planningEventStats(events []contracts.AgentDelta) (starts int, deltas int, ends int, markdown string) {
	var b strings.Builder
	for _, event := range events {
		switch typed := event.(type) {
		case contracts.DeltaPlanningStart:
			starts++
		case contracts.DeltaPlanningDelta:
			deltas++
			b.WriteString(typed.Delta)
		case contracts.DeltaPlanningEnd:
			ends++
		}
	}
	return starts, deltas, ends, b.String()
}
