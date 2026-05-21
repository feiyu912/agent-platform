package llm

import (
	"reflect"
	"testing"

	contracts "agent-platform/internal/contracts"
)

func TestResolveAgentModeCoder(t *testing.T) {
	if _, ok := resolveAgentMode("CODER").(coderMode); !ok {
		t.Fatalf("expected CODER to resolve to coderMode")
	}
}

func TestCoderPlanningStageToolsAreReadOnlyPlusQuestionsAndPlan(t *testing.T) {
	stream := &coderPlanningStream{}
	want := []string{"file_read", "file_grep", "datetime", "ask_user_question", "planning_write"}
	if got := stream.planStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("planStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderExecuteStageToolsExcludePlanningOnlyTools(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{
			ToolNames: []string{"bash", "file_read", "plan_add_tasks", "planning_write", "ask_user_question", "plan_update_task", "datetime"},
		},
	}
	want := []string{"bash", "file_read", "datetime"}
	if got := stream.executeStageTools(); !reflect.DeepEqual(got, want) {
		t.Fatalf("executeStageTools()=%#v want %#v", got, want)
	}
}

func TestCoderPlanningConfirmationUsesApprovalMode(t *testing.T) {
	stream := &coderPlanningStream{
		session: contracts.QuerySession{RunID: "run_1"},
		execCtx: &contracts.ExecutionContext{
			Budget: contracts.Budget{Tool: contracts.RetryPolicy{TimeoutMs: 120000}},
		},
	}
	ask := stream.planConfirmationAsk()
	if ask.Mode != "approval" || ask.ViewportType != "builtin" || ask.ViewportKey != "approval" {
		t.Fatalf("expected approval confirmation ask, got %#v", ask)
	}
	if len(ask.Questions) != 0 || len(ask.Approvals) != 1 {
		t.Fatalf("expected one approval and no questions, got %#v", ask)
	}
	approval, _ := ask.Approvals[0].(map[string]any)
	if approval["id"] != "confirm" {
		t.Fatalf("unexpected approval item %#v", approval)
	}
	options, _ := approval["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("expected approve/reject options, got %#v", approval)
	}
	first, _ := options[0].(map[string]any)
	second, _ := options[1].(map[string]any)
	if first["decision"] != "approve" || second["decision"] != "reject" {
		t.Fatalf("expected explicit approval decisions, got %#v", options)
	}
}
