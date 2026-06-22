package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	eventmemory "github.com/feiyu912/zenforge/eventlog/memory"
)

type zenForgeTestTools struct {
	definitions []api.ToolDetailResponse
	invoke      func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error)
}

func (t zenForgeTestTools) Definitions() []api.ToolDetailResponse { return t.definitions }
func (t zenForgeTestTools) Invoke(ctx context.Context, name string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	if t.invoke == nil {
		return contracts.ToolExecutionResult{}, nil
	}
	return t.invoke(ctx, name, args, execCtx)
}

type zenForgeRoundTripper struct {
	mu        sync.Mutex
	responses []string
	requests  []*http.Request
	bodies    [][]byte
}

func (t *zenForgeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, _ := io.ReadAll(req.Body)
	t.requests = append(t.requests, req.Clone(req.Context()))
	t.bodies = append(t.bodies, body)
	if len(t.responses) == 0 {
		return nil, errors.New("unexpected network request")
	}
	response := t.responses[0]
	t.responses = t.responses[1:]
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK",
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(response)), Request: req,
	}, nil
}

func TestZenForgeEngineStreamsHistoryPromptToolAndUsage(t *testing.T) {
	transport := &zenForgeRoundTripper{responses: []string{
		zenForgeSSE(
			`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"value\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		),
		zenForgeSSE(
			`{"choices":[{"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`,
		),
	}}
	var gotExecCtx *contracts.ExecutionContext
	tools := zenForgeTestTools{
		definitions: []api.ToolDetailResponse{{
			Key: "echo", Name: "echo", Description: "exact schema",
			Parameters: map[string]any{"type": "object", "required": []any{"value"}, "properties": map[string]any{"value": map[string]any{"type": "string"}}},
		}},
		invoke: func(_ context.Context, name string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
			gotExecCtx = execCtx
			if name != "echo" || args["value"] != "x" {
				t.Fatalf("unexpected tool invocation: %s %#v", name, args)
			}
			return contracts.ToolExecutionResult{
				Output: "stdout", Structured: map[string]any{"ok": true}, RawParams: map[string]any{"raw": true},
				HITL: map[string]any{"approved": true}, ExitCode: 0,
			}, nil
		},
	}
	engine, checkpoints := newZenForgeTestEngine(t, transport, tools, "OPENAI")
	session := zenForgeTestSession()
	session.HistoryMessages = []map[string]any{{"role": "user", "content": "old question"}, {"role": "assistant", "content": "old answer"}}
	session.SoulPrompt = "SOUL_MARKER"
	session.WorkspaceRoot = "/workspace"
	ctx := contracts.WithRunControl(context.Background(), contracts.NewRunControl(context.Background(), session.RunID))
	stream, err := engine.Stream(ctx, api.QueryRequest{RequestID: "req_1", RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey, Message: "new question"}, session)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	deltas := drainZenForgeDeltas(t, stream)
	if gotExecCtx == nil || gotExecCtx.Session.WorkspaceRoot != "/workspace" || gotExecCtx.CurrentToolID != "call_1" || gotExecCtx.RunControl == nil {
		t.Fatalf("tool execution context not preserved: %#v", gotExecCtx)
	}
	assertZenForgeDeltaTypes(t, deltas, contracts.DeltaToolCall{}, contracts.DeltaToolEnd{}, contracts.DeltaToolResult{}, contracts.DeltaContent{}, contracts.DeltaUsageSnapshot{}, contracts.DeltaFinishReason{})
	var result contracts.DeltaToolResult
	var usage contracts.DeltaUsageSnapshot
	for _, delta := range deltas {
		if value, ok := delta.(contracts.DeltaToolResult); ok {
			result = value
		}
		if value, ok := delta.(contracts.DeltaUsageSnapshot); ok {
			usage = value
		}
	}
	if result.Result.Output != "stdout" || result.Result.Structured["ok"] != true || result.Result.RawParams == nil || result.Result.HITL["approved"] != true {
		t.Fatalf("tool result lost fields: %#v", result.Result)
	}
	if usage.RunTotalTokens != 18 || usage.RunPromptTokens != 15 || usage.RunCompletionTokens != 3 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if _, err := checkpoints.Load(context.Background(), session.RunID); err != nil {
		t.Fatalf("checkpoint not persisted: %v", err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.bodies) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(transport.bodies))
	}
	if transport.requests[0].Header.Get("X-Custom") != "bridge" || transport.requests[0].Header.Get("Authorization") != "Bearer super-secret" {
		t.Fatalf("provider headers not preserved: %#v", transport.requests[0].Header)
	}
	if got := transport.requests[0].URL.String(); got != "https://provider.invalid/v1/chat/completions" {
		t.Fatalf("provider endpoint = %q", got)
	}
	var first map[string]any
	if err := json.Unmarshal(transport.bodies[0], &first); err != nil {
		t.Fatal(err)
	}
	messages := first["messages"].([]any)
	encoded, _ := json.Marshal(messages)
	if !strings.Contains(string(encoded), "SOUL_MARKER") || !strings.Contains(string(encoded), "old question") || !strings.Contains(string(encoded), "new question") {
		t.Fatalf("resolved prompt/history missing from request: %s", encoded)
	}
	toolSpecs := first["tools"].([]any)
	schema := toolSpecs[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("tool schema changed: %#v", schema)
	}
}

func TestZenForgeApprovalBridgeRoundTrip(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run_approval")
	bridge := newZenForgeApprovalBridge(control)
	request := approval.Request{
		ID: "await_1", RunID: "run_approval", ToolCallID: "call_1", Operation: "write", Title: "Write file",
		Risk: approval.RiskHigh, Options: approval.DefaultOptions(), CreatedAt: time.Now(),
	}
	result := make(chan approval.Decision, 1)
	errs := make(chan error, 1)
	go func() {
		decision, err := bridge.Request(context.Background(), request)
		if err != nil {
			errs <- err
			return
		}
		result <- decision
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := control.LookupAwaiting("await_1"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	raw := json.RawMessage(`{"id":"call_1","action":"approve","scope":"once","reason":"ok"}`)
	ack := control.ResolveSubmit(api.SubmitRequest{RunID: "run_approval", AwaitingID: "await_1", SubmitID: "submit_1", Params: api.SubmitParams{raw}})
	if !ack.Accepted {
		t.Fatalf("submit rejected: %#v", ack)
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	case decision := <-result:
		if decision.Action != approval.DecisionApprove || decision.RequestID != "await_1" {
			t.Fatalf("unexpected decision: %#v", decision)
		}
	case <-time.After(time.Second):
		t.Fatal("approval roundtrip timed out")
	}
	if _, _, ok := bridge.takeSubmit("await_1"); !ok {
		t.Fatal("submit metadata was not retained for delta mapping")
	}
}

func TestZenForgeApprovalRequestedSupportsImmediateSubmit(t *testing.T) {
	for i := 0; i < 500; i++ {
		runID := fmt.Sprintf("run_immediate_%d", i)
		awaitingID := fmt.Sprintf("await_immediate_%d", i)
		control := contracts.NewRunControl(context.Background(), runID)
		bridge := newZenForgeApprovalBridge(control)
		stream := &zenForgeAgentStream{
			control: control, approvals: bridge, session: contracts.QuerySession{RunID: runID},
		}
		request := approval.Request{
			ID: awaitingID, RunID: runID, ToolCallID: "call_immediate", Operation: "write", Risk: approval.RiskHigh,
			Options: approval.DefaultOptions(), CreatedAt: time.Now(),
		}
		deltas := stream.mapEvent(zenforge.NewEvent(zenforge.EventApprovalRequested, runID, map[string]any{
			"requestId": awaitingID, "request": request,
		}))
		if len(deltas) != 1 {
			t.Fatalf("iteration %d approval deltas = %#v", i, deltas)
		}
		ack := control.ResolveSubmit(api.SubmitRequest{
			RunID: runID, AwaitingID: awaitingID, SubmitID: fmt.Sprintf("submit_%d", i),
			Params: api.SubmitParams{json.RawMessage(`{"id":"call_immediate","decision":"approve"}`)},
		})
		if !ack.Accepted {
			t.Fatalf("iteration %d immediate submit unmatched: %#v", i, ack)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		decision, err := bridge.Request(ctx, request)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d broker did not consume pending submit: %v", i, err)
		}
		if decision.Action != approval.DecisionApprove {
			t.Fatalf("iteration %d decision = %#v", i, decision)
		}
	}
}

func TestZenForgeEngineApprovalRequiredRetriesOnlyAfterRuleApproval(t *testing.T) {
	transport := zenForgeApprovalTransport()
	var mu sync.Mutex
	invocations := 0
	sideEffect := false
	var firstExecCtx *contracts.ExecutionContext
	tools := zenForgeTestTools{
		definitions: []api.ToolDetailResponse{{
			Key: "file_write", Name: "file_write", Description: "write a file",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}}},
		}},
		invoke: func(_ context.Context, _ string, _ map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
			mu.Lock()
			defer mu.Unlock()
			invocations++
			if invocations == 1 {
				firstExecCtx = execCtx
				execCtx.SandboxSession = &contracts.SandboxSession{SessionID: "persisted"}
				execCtx.ReadFileState["/workspace/input.txt"] = contracts.ReadFileSnapshot{}
				return zenForgeFileWriteApprovalResult(), nil
			}
			if execCtx != firstExecCtx || execCtx.SandboxSession == nil || execCtx.SandboxSession.SessionID != "persisted" {
				t.Fatalf("execution context did not persist across approval retry: %#v", execCtx)
			}
			if _, ok := execCtx.ReadFileState["/workspace/input.txt"]; !ok {
				t.Fatal("read file state did not persist across approval retry")
			}
			if !execCtx.FileWriteRuleApprovals["file-write::workspace"] {
				t.Fatalf("rule approval was not registered before retry: %#v", execCtx.FileWriteRuleApprovals)
			}
			sideEffect = true
			return contracts.ToolExecutionResult{Output: "written", Structured: map[string]any{"ok": true}, ExitCode: 0}, nil
		},
	}
	engine, _ := newZenForgeTestEngine(t, transport, tools, "OPENAI")
	session := zenForgeTestSession()
	session.ToolNames = []string{"file_write"}
	control := contracts.NewRunControl(context.Background(), session.RunID)
	stream, err := engine.Stream(contracts.WithRunControl(context.Background(), control), api.QueryRequest{
		RequestID: session.RequestID, RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey, Message: "write it",
	}, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	deltas := collectZenForgeApprovalRun(t, stream, func(ask contracts.DeltaAwaitAsk) {
		mu.Lock()
		if sideEffect || invocations != 1 {
			t.Fatalf("tool side effect happened before approval: invocations=%d sideEffect=%v", invocations, sideEffect)
		}
		mu.Unlock()
		assertZenForgeApprovalAsk(t, ask)
		ack := control.ResolveSubmit(api.SubmitRequest{
			ChatID: session.ChatID, RunID: session.RunID, AgentKey: session.AgentKey, AwaitingID: ask.AwaitingID, SubmitID: "submit_rule",
			Params: api.SubmitParams{json.RawMessage(`{"id":"call_approval","decision":"approve_rule_run"}`)},
		})
		if !ack.Accepted {
			t.Fatalf("approval submit rejected: %#v", ack)
		}
	})
	mu.Lock()
	if invocations != 2 || !sideEffect {
		t.Fatalf("approved tool invocations=%d sideEffect=%v, want 2/true", invocations, sideEffect)
	}
	mu.Unlock()
	assertZenForgeDeltaOrder(t, deltas,
		contracts.DeltaToolCall{}, contracts.DeltaAwaitAsk{}, contracts.DeltaRequestSubmit{}, contracts.DeltaAwaitingAnswer{},
		contracts.DeltaToolResult{}, contracts.DeltaContent{}, contracts.DeltaUsageSnapshot{}, contracts.DeltaFinishReason{},
	)
	for _, delta := range deltas {
		switch value := delta.(type) {
		case contracts.DeltaRequestSubmit:
			if value.RequestID != session.RequestID {
				t.Fatalf("request submit request id = %q", value.RequestID)
			}
		case contracts.DeltaAwaitingAnswer:
			if value.Answer["decision"] != "approve_rule_run" {
				t.Fatalf("approval answer lost rule decision: %#v", value.Answer)
			}
		case contracts.DeltaToolResult:
			if value.Result.Output != "written" || value.Result.Error != "" {
				t.Fatalf("approval result polluted final tool result: %#v", value.Result)
			}
		}
	}
}

func TestZenForgeApprovalDecisionStrictMatching(t *testing.T) {
	req := approval.Request{ID: "await_1", ToolCallID: "call_1"}
	tests := []struct {
		name       string
		params     api.SubmitParams
		wantAction approval.DecisionAction
		wantScope  approval.DecisionScope
		wantMode   string
		wantError  string
	}{
		{
			name: "approve rule by tool call id", params: api.SubmitParams{json.RawMessage(`{"id":"call_1","decision":"approve_rule_run"}`)},
			wantAction: approval.DecisionAlways, wantScope: approval.ScopeRule, wantMode: "approve_rule_run",
		},
		{
			name: "reject by request id", params: api.SubmitParams{json.RawMessage(`{"id":"await_1","decision":"reject"}`)},
			wantAction: approval.DecisionReject, wantScope: approval.ScopeOnce, wantMode: "reject",
		},
		{name: "missing id", params: api.SubmitParams{json.RawMessage(`{"decision":"approve"}`)}, wantError: "no item matching"},
		{name: "malformed json", params: api.SubmitParams{json.RawMessage(`{"id":`)}, wantError: "decode zenforge approval submit item"},
		{name: "non object", params: api.SubmitParams{json.RawMessage(`"call_1"`)}, wantError: "must be an object"},
		{
			name: "multiple mismatched ids", params: api.SubmitParams{
				json.RawMessage(`{"id":"call_other","decision":"approve"}`),
				json.RawMessage(`{"id":"await_other","decision":"reject"}`),
			}, wantError: "no item matching",
		},
		{
			name: "duplicate match", params: api.SubmitParams{
				json.RawMessage(`{"id":"call_1","decision":"approve"}`),
				json.RawMessage(`{"id":"await_1","decision":"reject"}`),
			}, wantError: "duplicate matches",
		},
		{name: "unknown mode", params: api.SubmitParams{json.RawMessage(`{"id":"call_1","decision":"surprise"}`)}, wantError: "unsupported zenforge approval submit action"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, normalized, err := zenForgeApprovalDecision(req, contracts.SubmitResult{Request: api.SubmitRequest{Params: tt.params}})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decision.Action != tt.wantAction || decision.Scope != tt.wantScope || normalized != tt.wantMode {
				t.Fatalf("decision = %#v normalized=%q", decision, normalized)
			}
		})
	}
}

func TestZenForgeEngineRejectedApprovalDoesNotRetryTool(t *testing.T) {
	transport := zenForgeApprovalTransport()
	var mu sync.Mutex
	invocations := 0
	tools := zenForgeTestTools{
		definitions: []api.ToolDetailResponse{{Key: "file_write", Name: "file_write", Parameters: map[string]any{"type": "object"}}},
		invoke: func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
			mu.Lock()
			defer mu.Unlock()
			invocations++
			return zenForgeFileWriteApprovalResult(), nil
		},
	}
	engine, _ := newZenForgeTestEngine(t, transport, tools, "OPENAI")
	session := zenForgeTestSession()
	session.ToolNames = []string{"file_write"}
	control := contracts.NewRunControl(context.Background(), session.RunID)
	stream, err := engine.Stream(contracts.WithRunControl(context.Background(), control), api.QueryRequest{
		RequestID: session.RequestID, RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey, Message: "write it",
	}, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	deltas := collectZenForgeApprovalRun(t, stream, func(ask contracts.DeltaAwaitAsk) {
		ack := control.ResolveSubmit(api.SubmitRequest{
			RunID: session.RunID, AgentKey: session.AgentKey, AwaitingID: ask.AwaitingID, SubmitID: "submit_reject",
			Params: api.SubmitParams{json.RawMessage(`{"id":"call_approval","decision":"reject"}`)},
		})
		if !ack.Accepted {
			t.Fatalf("reject submit rejected: %#v", ack)
		}
	})
	mu.Lock()
	if invocations != 1 {
		t.Fatalf("rejected tool invoked %d times, want 1", invocations)
	}
	mu.Unlock()
	assertZenForgeDeltaTypes(t, deltas, contracts.DeltaAwaitAsk{}, contracts.DeltaAwaitingAnswer{}, contracts.DeltaToolResult{}, contracts.DeltaFinishReason{})
}

func TestZenForgeEngineInterruptWhileAwaitingApprovalStopsRun(t *testing.T) {
	transport := zenForgeApprovalTransport()
	var mu sync.Mutex
	invocations := 0
	tools := zenForgeTestTools{
		definitions: []api.ToolDetailResponse{{Key: "file_write", Name: "file_write", Parameters: map[string]any{"type": "object"}}},
		invoke: func(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
			mu.Lock()
			defer mu.Unlock()
			invocations++
			return zenForgeFileWriteApprovalResult(), nil
		},
	}
	engine, _ := newZenForgeTestEngine(t, transport, tools, "OPENAI")
	session := zenForgeTestSession()
	session.ToolNames = []string{"file_write"}
	control := contracts.NewRunControl(context.Background(), session.RunID)
	stream, err := engine.Stream(contracts.WithRunControl(context.Background(), control), api.QueryRequest{
		RequestID: session.RequestID, RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey, Message: "write it",
	}, session)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for {
		delta, nextErr := stream.Next()
		if nextErr != nil {
			t.Fatalf("Next before approval: %v", nextErr)
		}
		if _, ok := delta.(contracts.DeltaAwaitAsk); ok {
			break
		}
	}
	control.Interrupt(contracts.InterruptInfo{Reason: contracts.InterruptReasonUserCancelled})
	delta, err := stream.Next()
	if err != nil {
		t.Fatalf("Next after interrupt: %v", err)
	}
	if _, ok := delta.(contracts.DeltaRunCancel); !ok {
		t.Fatalf("interrupt delta = %T, want DeltaRunCancel", delta)
	}
	mu.Lock()
	defer mu.Unlock()
	if invocations != 1 {
		t.Fatalf("interrupted approval retried tool %d times", invocations)
	}
}

func TestZenForgeStreamCloseCancelsBlockedNext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan zenforge.Event)
	stream := newZenForgeAgentStream(ctx, cancel, events, contracts.NewRunControl(ctx, "run_close"), newZenForgeApprovalBridge(nil), &zenForgeToolResultCache{values: map[string]contracts.ToolExecutionResult{}}, &zenForgeUsageTracker{}, "req_close", contracts.QuerySession{RunID: "run_close"})
	done := make(chan error, 1)
	go func() { _, err := stream.Next(); done <- err }()
	time.Sleep(10 * time.Millisecond)
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			t.Fatalf("Next error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Next")
	}
}

func TestZenForgeRunControlInterruptCancelsStream(t *testing.T) {
	control := contracts.NewRunControl(context.Background(), "run_interrupt")
	events := make(chan zenforge.Event)
	stream := newZenForgeAgentStream(control.Context(), func() {}, events, control, newZenForgeApprovalBridge(control), &zenForgeToolResultCache{values: map[string]contracts.ToolExecutionResult{}}, &zenForgeUsageTracker{}, "req_interrupt", contracts.QuerySession{RunID: "run_interrupt"})
	done := make(chan contracts.AgentDelta, 1)
	go func() {
		delta, _ := stream.Next()
		done <- delta
	}()
	control.Interrupt(contracts.InterruptInfo{Reason: contracts.InterruptReasonUserCancelled})
	select {
	case delta := <-done:
		if _, ok := delta.(contracts.DeltaRunCancel); !ok {
			t.Fatalf("interrupt delta = %T", delta)
		}
	case <-time.After(time.Second):
		t.Fatal("interrupt did not cancel stream")
	}
}

func TestZenForgeCoderModeMapsToSupportedRuntime(t *testing.T) {
	if got, err := zenForgeMode(contracts.QuerySession{Mode: "CODER"}); err != nil || got != "REACT" {
		t.Fatalf("coder mode = %q", got)
	}
	if got, err := zenForgeMode(contracts.QuerySession{Mode: "CODER", PlanningMode: true}); err != nil || got != "PLAN_EXECUTE" {
		t.Fatalf("coder planning mode = %q", got)
	}
}

func TestZenForgeUnknownModeFailsClosed(t *testing.T) {
	if _, err := zenForgeMode(contracts.QuerySession{Mode: "mystery"}); err == nil {
		t.Fatal("unknown mode should fail closed")
	}
	engine, _ := newZenForgeTestEngine(t, &zenForgeRoundTripper{}, zenForgeTestTools{}, "OPENAI")
	session := zenForgeTestSession()
	session.Mode = "mystery"
	if _, err := engine.Stream(context.Background(), api.QueryRequest{
		RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey, Message: "hello",
	}, session); err == nil || !strings.Contains(err.Error(), "unsupported zenforge agent mode") {
		t.Fatalf("Stream unknown mode error = %v", err)
	}
}

func TestZenForgePlanExecuteResolvedPromptIncludesEachStageOnce(t *testing.T) {
	session := zenForgeTestSession()
	session.Mode = "PLAN_EXECUTE"
	session.SoulPrompt = "BASE_PROMPT_MARKER"
	session.PlanPrompt = "PLAN_STAGE_MARKER"
	session.ExecutePrompt = "EXECUTE_STAGE_MARKER"
	session.SummaryPrompt = "SUMMARY_STAGE_MARKER"
	prompt := zenForgeResolvedPrompt(session, api.QueryRequest{Message: "work"}, nil, "PLAN_EXECUTE")
	for _, marker := range []string{"BASE_PROMPT_MARKER", "PLAN_STAGE_MARKER", "EXECUTE_STAGE_MARKER", "SUMMARY_STAGE_MARKER"} {
		if count := strings.Count(prompt, marker); count != 1 {
			t.Fatalf("%s occurs %d times in resolved prompt: %s", marker, count, prompt)
		}
	}
}

func TestZenForgeFinalAssistantContentAllowsEmptyCompletedOutput(t *testing.T) {
	stream := &zenForgeAgentStream{cancel: func() {}, session: contracts.QuerySession{RunID: "run_empty"}}
	stream.mapEvent(zenforge.NewEvent(zenforge.EventModelDelta, "run_empty", map[string]any{"textDelta": "stale"}))
	stream.mapEvent(zenforge.NewEvent(zenforge.EventRunDone, "run_empty", map[string]any{"output": ""}))
	if value, ok := stream.FinalAssistantContent(); !ok || value != "" {
		t.Fatalf("empty completed output = %q, %v", value, ok)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if value, ok := stream.FinalAssistantContent(); !ok || value != "" {
		t.Fatalf("empty completed output after close = %q, %v", value, ok)
	}
}

func TestZenForgeRejectsIdentityMismatch(t *testing.T) {
	err := validateZenForgeIdentity(api.QueryRequest{RunID: "request-run"}, contracts.QuerySession{RunID: "session-run"})
	if err == nil || !strings.Contains(err.Error(), "run id mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestZenForgeEngineResumesExistingCheckpoint(t *testing.T) {
	transport := &zenForgeRoundTripper{responses: []string{zenForgeSSE(
		`{"choices":[{"delta":{"role":"assistant","content":"persisted"},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
	)}}
	engine, _ := newZenForgeTestEngine(t, transport, zenForgeTestTools{}, "OPENAI")
	session := zenForgeTestSession()
	req := api.QueryRequest{RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey, Message: "hello"}
	first, err := engine.Stream(context.Background(), req, session)
	if err != nil {
		t.Fatal(err)
	}
	drainZenForgeDeltas(t, first)
	second, err := engine.Stream(context.Background(), req, session)
	if err != nil {
		t.Fatal(err)
	}
	deltas := drainZenForgeDeltas(t, second)
	assertZenForgeDeltaTypes(t, deltas, contracts.DeltaFinishReason{})
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 1 {
		t.Fatalf("resume made %d provider requests, want 1 total", len(transport.requests))
	}
}

func TestZenForgeEngineRejectsUnsupportedProtocol(t *testing.T) {
	engine, _ := newZenForgeTestEngine(t, &zenForgeRoundTripper{}, zenForgeTestTools{}, "CUSTOM")
	_, err := engine.resolveZenForgeModel(context.Background(), "mock-model")
	if err == nil || !strings.Contains(err.Error(), "unsupported zenforge provider protocol") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestZenForgeEngineResolvesAnthropicAdapter(t *testing.T) {
	engine, _ := newZenForgeTestEngine(t, &zenForgeRoundTripper{}, zenForgeTestTools{}, "ANTHROPIC")
	resolved, err := engine.resolveZenForgeModel(context.Background(), "mock-model")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%T", resolved); got != "*anthropic.Client" {
		t.Fatalf("resolved model = %s", got)
	}
}

func TestZenForgeUnmappedEventBecomesDeltaError(t *testing.T) {
	stream := &zenForgeAgentStream{session: contracts.QuerySession{RunID: "run_1"}}
	deltas := stream.mapEvent(zenforge.NewEvent(zenforge.EventType("future.event"), "run_1", nil))
	if len(deltas) != 1 {
		t.Fatalf("deltas = %#v", deltas)
	}
	if _, ok := deltas[0].(contracts.DeltaError); !ok {
		t.Fatalf("delta = %T, want DeltaError", deltas[0])
	}
}

func TestZenForgeMapsPlanningApprovalTaskAndTerminalEvents(t *testing.T) {
	approvals := newZenForgeApprovalBridge(nil)
	approvals.submits["await_1"] = contracts.SubmitResult{Request: api.SubmitRequest{
		ChatID: "chat_1", RunID: "run_1", AwaitingID: "await_1", SubmitID: "submit_1",
		Params: api.SubmitParams{json.RawMessage(`{"id":"await_1","action":"approve"}`)},
	}}
	stream := &zenForgeAgentStream{
		approvals: approvals, toolResults: &zenForgeToolResultCache{values: map[string]contracts.ToolExecutionResult{}},
		usage: &zenForgeUsageTracker{}, session: contracts.QuerySession{RunID: "run_1", ChatID: "chat_1"},
	}
	tests := []struct {
		event zenforge.Event
		want  []contracts.AgentDelta
	}{
		{zenforge.NewEvent(zenforge.EventTodoUpdated, "run_1", map[string]any{"todos": []any{map[string]any{"id": "t1"}}}), []contracts.AgentDelta{contracts.DeltaPlanUpdate{}}},
		{zenforge.NewEvent(zenforge.EventTaskStarted, "run_1", map[string]any{"todoId": "t1", "content": "work"}), []contracts.AgentDelta{contracts.DeltaTaskLifecycle{}}},
		{zenforge.NewEvent(zenforge.EventSubtaskStarted, "run_1", map[string]any{"subtaskId": "s1", "agentName": "worker"}), []contracts.AgentDelta{contracts.DeltaTaskLifecycle{}}},
		{zenforge.NewEvent(zenforge.EventApprovalRequested, "run_1", map[string]any{"requestId": "await_1", "request": map[string]any{"risk": "high"}}), []contracts.AgentDelta{contracts.DeltaAwaitAsk{}}},
		{zenforge.NewEvent(zenforge.EventApprovalResolved, "run_1", map[string]any{"requestId": "await_1", "action": "approve"}), []contracts.AgentDelta{contracts.DeltaRequestSubmit{}, contracts.DeltaAwaitingAnswer{}}},
		{zenforge.NewEvent(zenforge.EventRunCancelled, "run_1", nil), []contracts.AgentDelta{contracts.DeltaRunCancel{}}},
		{zenforge.NewEvent(zenforge.EventRunError, "run_1", map[string]any{"error": "failed"}), []contracts.AgentDelta{contracts.DeltaError{}}},
	}
	for _, test := range tests {
		got := stream.mapEvent(test.event)
		if len(got) != len(test.want) {
			t.Fatalf("%s delta count = %d, want %d: %#v", test.event.Type, len(got), len(test.want), got)
		}
		for i := range got {
			if fmt.Sprintf("%T", got[i]) != fmt.Sprintf("%T", test.want[i]) {
				t.Fatalf("%s delta[%d] = %T, want %T", test.event.Type, i, got[i], test.want[i])
			}
		}
	}
}

func TestZenForgePlanExecuteMapsInitialModelDeltaAsPlanning(t *testing.T) {
	stream := &zenForgeAgentStream{planning: true, session: contracts.QuerySession{RunID: "run_1", ChatID: "chat_1"}}
	first := stream.mapEvent(zenforge.NewEvent(zenforge.EventModelDelta, "run_1", map[string]any{"textDelta": "plan"}))
	if _, ok := first[0].(contracts.DeltaPlanningDelta); !ok {
		t.Fatalf("initial delta = %T", first[0])
	}
	stream.mapEvent(zenforge.NewEvent(zenforge.EventTodoUpdated, "run_1", map[string]any{"todos": []any{}}))
	second := stream.mapEvent(zenforge.NewEvent(zenforge.EventModelDelta, "run_1", map[string]any{"textDelta": "answer"}))
	if _, ok := second[0].(contracts.DeltaContent); !ok {
		t.Fatalf("post-plan delta = %T", second[0])
	}
}

func TestZenForgeExplicitlyIgnoresNestedSubtaskProgress(t *testing.T) {
	stream := &zenForgeAgentStream{}
	got := stream.mapEvent(zenforge.NewEvent(zenforge.EventSubtaskEvent, "run_1", map[string]any{"childEventType": "model.delta"}))
	if got != nil {
		t.Fatalf("nested progress should be explicitly ignored, got %#v", got)
	}
}

func newZenForgeTestEngine(t *testing.T, transport http.RoundTripper, tools contracts.ToolExecutor, protocol string) (*ZenForgeAgentEngine, *checkpointmemory.Store) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "providers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := "key: mock\nbaseUrl: https://provider.invalid\napiKey: super-secret\ndefaultModel: mock-id\nprotocols:\n  " + protocol + ":\n    endpointPath: /v1/" + map[string]string{"OPENAI": "chat/completions", "ANTHROPIC": "messages"}[protocol] + "\n    headers:\n      X-Custom: bridge\n"
	model := "key: mock-model\nprovider: mock\nprotocol: " + protocol + "\nmodelId: mock-id\n"
	if err := os.WriteFile(filepath.Join(root, "providers", "mock.yml"), []byte(provider), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "models", "mock.yml"), []byte(model), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := models.LoadModelRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := checkpointmemory.New()
	engine, err := NewZenForgeAgentEngine(ZenForgeEngineConfig{
		Models: registry, Tools: tools, HTTPClient: &http.Client{Transport: transport},
		Checkpoints: checkpoints, Events: eventmemory.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine, checkpoints
}

func zenForgeTestSession() contracts.QuerySession {
	return contracts.QuerySession{
		RequestID: "req_1", RunID: "run_1", ChatID: "chat_1", AgentKey: "agent_1", AgentName: "Agent",
		ModelKey: "mock-model", ToolNames: []string{"echo"}, Mode: "react", ReactMaxSteps: 4,
		ResolvedBudget: contracts.Budget{Model: contracts.RetryPolicy{MaxCalls: 8}, Tool: contracts.RetryPolicy{MaxCalls: 8}},
	}
}

func zenForgeSSE(lines ...string) string {
	var out strings.Builder
	for _, line := range lines {
		out.WriteString("data: ")
		out.WriteString(line)
		out.WriteString("\n\n")
	}
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}

func zenForgeApprovalTransport() *zenForgeRoundTripper {
	return &zenForgeRoundTripper{responses: []string{
		zenForgeSSE(
			`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_approval","type":"function","function":{"name":"file_write","arguments":"{\"file_path\":\"/workspace/output.txt\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`,
		),
		zenForgeSSE(
			`{"choices":[{"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		),
	}}
}

func zenForgeFileWriteApprovalResult() contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{
		Output: "write approval required",
		Structured: map[string]any{
			"error": "file_write_approval_required", "message": "write requires approval",
			"filePath": "/workspace/output.txt", "command": "file_write /workspace/output.txt",
			"fingerprint": "write-fingerprint", "ruleKey": "file-write::workspace",
		},
		Error: "file_write_approval_required", ExitCode: -1,
	}
}

func collectZenForgeApprovalRun(t *testing.T, stream contracts.AgentStream, onAsk func(contracts.DeltaAwaitAsk)) []contracts.AgentDelta {
	t.Helper()
	var out []contracts.AgentDelta
	for {
		delta, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, delta)
		if ask, ok := delta.(contracts.DeltaAwaitAsk); ok {
			onAsk(ask)
		}
	}
}

func assertZenForgeApprovalAsk(t *testing.T, ask contracts.DeltaAwaitAsk) {
	t.Helper()
	if ask.ViewportType != "builtin" || ask.ViewportKey != "approval" || ask.RunID == "" || len(ask.Approvals) != 1 {
		t.Fatalf("approval ask does not match native viewport contract: %#v", ask)
	}
	payload, _ := ask.Approvals[0].(map[string]any)
	options, _ := payload["options"].([]any)
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if option["decision"] == "approve_rule_run" {
			return
		}
	}
	t.Fatalf("approval ask lacks approve_rule_run option: %#v", ask.Approvals)
}

func assertZenForgeDeltaOrder(t *testing.T, values []contracts.AgentDelta, expected ...contracts.AgentDelta) {
	t.Helper()
	index := 0
	for _, got := range values {
		if index < len(expected) && fmt.Sprintf("%T", got) == fmt.Sprintf("%T", expected[index]) {
			index++
		}
	}
	if index != len(expected) {
		t.Fatalf("delta order matched %d/%d; got %#v", index, len(expected), values)
	}
}

func drainZenForgeDeltas(t *testing.T, stream contracts.AgentStream) []contracts.AgentDelta {
	t.Helper()
	defer stream.Close()
	var out []contracts.AgentDelta
	for {
		delta, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, delta)
	}
}

func assertZenForgeDeltaTypes(t *testing.T, values []contracts.AgentDelta, expected ...contracts.AgentDelta) {
	t.Helper()
	for _, want := range expected {
		found := false
		for _, got := range values {
			if fmt.Sprintf("%T", got) == fmt.Sprintf("%T", want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing delta %T in %#v", want, values)
		}
	}
}
