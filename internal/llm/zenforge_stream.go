package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
)

type zenForgeApprovalBridge struct {
	control   *contracts.RunControl
	mu        sync.Mutex
	submits   map[string]contracts.SubmitResult
	decisions map[string]string
}

func newZenForgeApprovalBridge(control *contracts.RunControl) *zenForgeApprovalBridge {
	return &zenForgeApprovalBridge{control: control, submits: map[string]contracts.SubmitResult{}, decisions: map[string]string{}}
}

func (b *zenForgeApprovalBridge) Request(ctx context.Context, req approval.Request) (approval.Decision, error) {
	if b.control == nil {
		return approval.Decision{}, contracts.ErrRunControlUnavailable
	}
	timeout := int64(0)
	if req.ExpiresAt != nil {
		timeout = max(0, int64(time.Until(*req.ExpiresAt).Seconds()))
	}
	b.control.ExpectSubmit(contracts.AwaitingSubmitContext{AwaitingID: req.ID, Mode: "approval", ItemCount: 1, Timeout: timeout, NoTimeout: timeout == 0})
	defer b.control.ClearExpectedSubmit(req.ID)
	b.control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	submit, err := b.control.AwaitSubmitWithTimeout(ctx, req.ID, time.Duration(timeout)*time.Second)
	if err != nil {
		return approval.Decision{}, err
	}
	b.control.TransitionState(contracts.RunLoopStateToolExecuting)
	decision, platformDecision, err := zenForgeApprovalDecision(req, submit)
	if err != nil {
		return approval.Decision{}, fmt.Errorf("resolve zenforge approval %q: %w", req.ID, err)
	}
	b.mu.Lock()
	b.submits[req.ID] = submit
	b.decisions[req.ID] = platformDecision
	b.mu.Unlock()
	return decision, nil
}

func (b *zenForgeApprovalBridge) takeSubmit(id string) (contracts.SubmitResult, string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.submits[id]
	decision := b.decisions[id]
	delete(b.submits, id)
	delete(b.decisions, id)
	return value, decision, ok
}

func zenForgeApprovalDecision(req approval.Request, submit contracts.SubmitResult) (approval.Decision, string, error) {
	payload, err := zenForgeApprovalSubmitItem(req, submit.Request.Params)
	if err != nil {
		return approval.Decision{}, "", err
	}
	action := strings.ToLower(firstNonBlank(
		contracts.AnyStringNode(payload["action"]), contracts.AnyStringNode(payload["decision"]),
		contracts.AnyStringNode(payload["status"]),
	))
	platformDecision := action
	scope := approval.DecisionScope(contracts.AnyStringNode(payload["scope"]))
	switch action {
	case "approve", "approved", "accept", "accepted", "allow":
		action = string(approval.DecisionApprove)
	case "always":
		action = string(approval.DecisionAlways)
		if scope == "" {
			scope = approval.ScopeRule
		}
	case "approve_rule_run", "approve_always":
		action = string(approval.DecisionAlways)
		scope = approval.ScopeRule
	case "abort", "cancel":
		action = string(approval.DecisionAbort)
	case "reject", "rejected", "deny", "denied":
		action = string(approval.DecisionReject)
	default:
		return approval.Decision{}, "", fmt.Errorf("unsupported zenforge approval submit action %q", action)
	}
	if scope == "" {
		scope = approval.ScopeOnce
	}
	return approval.Decision{
		RequestID: req.ID, Action: approval.DecisionAction(action), Scope: scope,
		Reason: contracts.AnyStringNode(payload["reason"]), Payload: contracts.CloneAnyMap(payload), DecidedAt: time.Now().UTC(),
	}, platformDecision, nil
}

func zenForgeApprovalSubmitItem(req approval.Request, params api.SubmitParams) (map[string]any, error) {
	var matched map[string]any
	for index, raw := range params {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode zenforge approval submit item %d: %w", index, err)
		}
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("zenforge approval submit item %d must be an object", index)
		}
		id := strings.TrimSpace(contracts.AnyStringNode(item["id"]))
		if id == "" || (id != req.ToolCallID && id != req.ID) {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("zenforge approval submit contains duplicate matches for %q", id)
		}
		matched = item
	}
	if matched == nil {
		return nil, fmt.Errorf("zenforge approval submit has no item matching tool call %q or request %q", req.ToolCallID, req.ID)
	}
	return matched, nil
}

type zenForgeAgentStream struct {
	ctx         context.Context
	cancel      context.CancelFunc
	events      <-chan zenforge.Event
	control     *contracts.RunControl
	approvals   *zenForgeApprovalBridge
	toolResults *zenForgeToolResultCache
	usage       *zenForgeUsageTracker
	session     contracts.QuerySession
	requestID   string

	nextMu       sync.Mutex
	stateMu      sync.Mutex
	pending      []contracts.AgentDelta
	closed       bool
	finalContent strings.Builder
	runDone      bool
	cancelOnce   sync.Once
	planning     bool
	toolCalls    int
}

var _ contracts.OrchestratableAgentStream = (*zenForgeAgentStream)(nil)

func newZenForgeAgentStream(ctx context.Context, cancel context.CancelFunc, events <-chan zenforge.Event, control *contracts.RunControl, approvals *zenForgeApprovalBridge, toolResults *zenForgeToolResultCache, usage *zenForgeUsageTracker, requestID string, session contracts.QuerySession) *zenForgeAgentStream {
	mode := strings.ToUpper(strings.TrimSpace(session.Mode))
	return &zenForgeAgentStream{ctx: ctx, cancel: cancel, events: events, control: control, approvals: approvals, toolResults: toolResults, usage: usage, requestID: requestID, session: session, planning: mode == "PLAN_EXECUTE" || mode == "PLAN-EXECUTE" || (mode == "CODER" && session.PlanningMode)}
}

func (s *zenForgeAgentStream) Next() (contracts.AgentDelta, error) {
	s.nextMu.Lock()
	defer s.nextMu.Unlock()
	s.stateMu.Lock()
	if len(s.pending) > 0 {
		next := s.pending[0]
		s.pending = s.pending[1:]
		s.stateMu.Unlock()
		return next, nil
	}
	if s.closed {
		s.stateMu.Unlock()
		return nil, io.EOF
	}
	s.stateMu.Unlock()
	for {
		select {
		case <-s.ctx.Done():
			s.stateMu.Lock()
			s.closed = true
			s.stateMu.Unlock()
			if s.control != nil && s.control.Interrupted() {
				return contracts.DeltaRunCancel{RunID: s.session.RunID}, nil
			}
			return nil, s.ctx.Err()
		case event, ok := <-s.events:
			if !ok {
				s.stateMu.Lock()
				s.closed = true
				s.stateMu.Unlock()
				s.cancelOnce.Do(s.cancel)
				return nil, io.EOF
			}
			deltas := s.mapEvent(event)
			if len(deltas) == 0 {
				continue
			}
			s.stateMu.Lock()
			s.pending = append(s.pending, deltas[1:]...)
			s.stateMu.Unlock()
			return deltas[0], nil
		}
	}
}

func (s *zenForgeAgentStream) Close() error {
	s.stateMu.Lock()
	s.closed = true
	s.cancelOnce.Do(s.cancel)
	s.stateMu.Unlock()
	return nil
}

func (s *zenForgeAgentStream) InjectToolResult(string, string, bool) bool { return false }
func (s *zenForgeAgentStream) FinalAssistantContent() (string, bool) {
	s.nextMu.Lock()
	defer s.nextMu.Unlock()
	value := s.finalContent.String()
	return value, s.runDone
}

func (s *zenForgeAgentStream) mapEvent(event zenforge.Event) []contracts.AgentDelta {
	p := event.Payload
	switch event.Type {
	case zenforge.EventRunStarted, zenforge.EventRunResumed, zenforge.EventStepStarted, zenforge.EventStepDone,
		zenforge.EventModelStarted, zenforge.EventCheckpointCreated, zenforge.EventWorkspaceChanged:
		return nil
	case zenforge.EventModelDelta:
		text := contracts.AnyStringNode(p["textDelta"])
		if s.planning {
			return []contracts.AgentDelta{contracts.DeltaPlanningDelta{PlanningID: event.RunID() + "_planning", Delta: text}}
		}
		s.finalContent.WriteString(text)
		return []contracts.AgentDelta{contracts.DeltaContent{Text: text}}
	case zenforge.EventModelDone:
		last, total, calls := s.usage.snapshot()
		return []contracts.AgentDelta{contracts.DeltaUsageSnapshot{
			ChatID: s.session.ChatID, ModelKey: s.session.ModelKey,
			LLMReturnPromptTokens: last.PromptTokens, LLMReturnCompletionTokens: last.CompletionTokens, LLMReturnTotalTokens: last.TotalTokens,
			LLMReturnLLMChatCompletionCount: 1, LLMReturnToolCallCount: contracts.AnyIntNode(p["toolCallCount"]),
			RunPromptTokens: total.PromptTokens, RunCompletionTokens: total.CompletionTokens, RunTotalTokens: total.TotalTokens,
			RunLLMChatCompletionCount: calls, RunToolCallCount: s.toolCalls,
		}}
	case zenforge.EventToolCall:
		s.toolCalls++
		id, name := contracts.AnyStringNode(p["toolCallId"]), contracts.AnyStringNode(p["toolName"])
		args := zenForgeJSON(p["arguments"])
		return []contracts.AgentDelta{
			contracts.DeltaToolCall{ID: id, Name: name, ArgsDelta: args},
			contracts.DeltaToolEnd{ToolIDs: []string{id}},
		}
	case zenforge.EventToolResult, zenforge.EventToolError:
		id := contracts.AnyStringNode(p["toolCallId"])
		result, ok := s.toolResults.take(id)
		if !ok {
			result = contracts.ToolExecutionResult{
				Output: contracts.AnyStringNode(p["output"]), Structured: contracts.CloneAnyMap(contracts.AnyMapNode(p["structured"])),
				Error: contracts.AnyStringNode(p["error"]), ExitCode: contracts.AnyIntNode(p["exitCode"]),
			}
		}
		return []contracts.AgentDelta{contracts.DeltaToolResult{ToolID: id, ToolName: contracts.AnyStringNode(p["toolName"]), Result: result}}
	case zenforge.EventApprovalRequested:
		id := contracts.AnyStringNode(p["requestId"])
		timeout := zenForgeApprovalTimeout(p)
		if s.control != nil {
			s.control.ExpectSubmit(contracts.AwaitingSubmitContext{
				AwaitingID: id, Mode: "approval", ItemCount: 1, Timeout: timeout, NoTimeout: timeout == 0,
			})
		}
		approvalPayload := zenForgeApprovalPayload(p)
		return []contracts.AgentDelta{contracts.DeltaAwaitAsk{
			AwaitingID: id, Mode: "approval", RunID: event.RunID(), ViewportType: "builtin", ViewportKey: "approval",
			Timeout: timeout, Approvals: []any{approvalPayload},
		}}
	case zenforge.EventApprovalResolved, zenforge.EventApprovalExpired:
		id := contracts.AnyStringNode(p["requestId"])
		deltas := []contracts.AgentDelta{}
		platformDecision := contracts.AnyStringNode(p["action"])
		if submit, decision, ok := s.approvals.takeSubmit(id); ok {
			if decision != "" {
				platformDecision = decision
			}
			deltas = append(deltas, contracts.DeltaRequestSubmit{
				RequestID: s.requestID,
				ChatID:    submit.Request.ChatID, RunID: submit.Request.RunID,
				AwaitingID: id, SubmitID: submit.Request.SubmitID, Params: submit.Request.Params,
			})
		}
		answer := map[string]any{"status": "answered", "mode": "approval", "decision": platformDecision, "id": p["toolCallId"], "scope": p["scope"], "reason": p["reason"]}
		if event.Type == zenforge.EventApprovalExpired {
			answer = contracts.AwaitingErrorAnswer("approval", "timeout", contracts.AnyStringNode(p["reason"]))
		}
		deltas = append(deltas, contracts.DeltaAwaitingAnswer{AwaitingID: id, Answer: answer})
		return deltas
	case zenforge.EventTodoUpdated:
		s.planning = false
		return []contracts.AgentDelta{contracts.DeltaPlanUpdate{PlanID: event.RunID() + "_plan", Plan: p["todos"], ChatID: s.session.ChatID}}
	case zenforge.EventTaskStarted, zenforge.EventTaskDone, zenforge.EventTaskError, zenforge.EventTaskCancelled:
		return []contracts.AgentDelta{zenForgeTaskDelta(event)}
	case zenforge.EventSubtaskStarted, zenforge.EventSubtaskDone, zenforge.EventSubtaskError:
		return []contracts.AgentDelta{zenForgeSubtaskDelta(event)}
	case zenforge.EventSubtaskEvent:
		// Platform has no child-progress AgentDelta. Start and terminal lifecycle
		// events remain visible; the nested child event is deliberately ignored.
		return nil
	case zenforge.EventRunDone:
		output := contracts.AnyStringNode(p["output"])
		s.finalContent.Reset()
		s.finalContent.WriteString(output)
		s.runDone = true
		return []contracts.AgentDelta{contracts.DeltaFinishReason{Reason: "stop"}}
	case zenforge.EventRunCancelled:
		return []contracts.AgentDelta{contracts.DeltaRunCancel{RunID: event.RunID()}}
	case zenforge.EventRunError:
		return []contracts.AgentDelta{contracts.DeltaError{Error: map[string]any{"code": "zenforge_run_error", "message": contracts.AnyStringNode(p["error"])}}}
	default:
		return []contracts.AgentDelta{contracts.DeltaError{Error: map[string]any{"code": "zenforge_unmapped_event", "message": fmt.Sprintf("unmapped ZenForge event %q", event.Type)}}}
	}
}

func zenForgeApprovalPayload(payload map[string]any) map[string]any {
	out := map[string]any{}
	data, err := json.Marshal(payload["request"])
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	out["requestId"] = payload["requestId"]
	out["id"] = payload["toolCallId"]
	out["toolName"] = payload["toolName"]
	out["operation"] = payload["operation"]
	out["risk"] = payload["risk"]
	options, _ := out["options"].([]any)
	mapped := make([]any, 0, len(options))
	for _, value := range options {
		option, _ := value.(map[string]any)
		action := strings.ToLower(contracts.AnyStringNode(option["action"]))
		decision := action
		if action == string(approval.DecisionAlways) {
			decision = "approve_rule_run"
		}
		mapped = append(mapped, map[string]any{
			"label": option["label"], "description": option["description"], "decision": decision,
		})
	}
	out["options"] = mapped
	return out
}

func zenForgeApprovalTimeout(payload map[string]any) int64 {
	data, err := json.Marshal(payload["request"])
	if err != nil {
		return 0
	}
	var req approval.Request
	if json.Unmarshal(data, &req) != nil || req.ExpiresAt == nil {
		return 0
	}
	return max(0, int64(time.Until(*req.ExpiresAt).Seconds()))
}

func zenForgeTaskDelta(event zenforge.Event) contracts.DeltaTaskLifecycle {
	kind := map[zenforge.EventType]string{zenforge.EventTaskStarted: "start", zenforge.EventTaskDone: "complete", zenforge.EventTaskError: "error", zenforge.EventTaskCancelled: "cancel"}[event.Type]
	return contracts.DeltaTaskLifecycle{Kind: kind, TaskID: contracts.AnyStringNode(event.Payload["todoId"]), RunID: event.RunID(), Description: contracts.AnyStringNode(event.Payload["content"]), Reason: contracts.AnyStringNode(event.Payload["error"]), Error: contracts.CloneAnyMap(event.Payload)}
}

func zenForgeSubtaskDelta(event zenforge.Event) contracts.DeltaTaskLifecycle {
	kind := map[zenforge.EventType]string{zenforge.EventSubtaskStarted: "start", zenforge.EventSubtaskDone: "complete", zenforge.EventSubtaskError: "error"}[event.Type]
	return contracts.DeltaTaskLifecycle{Kind: kind, TaskID: contracts.AnyStringNode(event.Payload["subtaskId"]), RunID: firstNonBlank(contracts.AnyStringNode(event.Payload["runId"]), event.RunID()), TaskName: contracts.AnyStringNode(event.Payload["name"]), SubAgentKey: contracts.AnyStringNode(event.Payload["agentName"]), Reason: contracts.AnyStringNode(event.Payload["error"]), Error: contracts.CloneAnyMap(event.Payload)}
}

func zenForgeJSON(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.RawMessage:
		return string(value)
	default:
		data, _ := json.Marshal(value)
		return string(data)
	}
}
