package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

type zenForgeTool struct{ definition api.ToolDetailResponse }

func (t zenForgeTool) Name() string {
	if strings.TrimSpace(t.definition.Name) != "" {
		return strings.TrimSpace(t.definition.Name)
	}
	return strings.TrimSpace(t.definition.Key)
}
func (t zenForgeTool) Description() string { return t.definition.Description }
func (t zenForgeTool) Schema() map[string]any {
	return contracts.CloneAnyMap(t.definition.Parameters)
}
func (t zenForgeTool) Call(context.Context, json.RawMessage, tool.Context) (tool.Result, error) {
	return tool.Result{}, fmt.Errorf("platform tools must be invoked through the bridge invoker")
}

type zenForgeToolResultCache struct {
	mu     sync.Mutex
	values map[string]contracts.ToolExecutionResult
}

func (c *zenForgeToolResultCache) put(id string, result contracts.ToolExecutionResult) {
	c.mu.Lock()
	c.values[id] = result
	c.mu.Unlock()
}

func (c *zenForgeToolResultCache) take(id string) (contracts.ToolExecutionResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, ok := c.values[id]
	delete(c.values, id)
	return result, ok
}

type zenForgeApprovalRecord struct {
	Request     approval.Request
	Kind        string
	Fingerprint string
	RuleKey     string
}

type zenForgeExecutionState struct {
	mu            sync.Mutex
	execCtx       *contracts.ExecutionContext
	requests      map[string]zenForgeApprovalRecord
	approvedRules map[string]bool
}

func newZenForgeExecutionState(req api.QueryRequest, session contracts.QuerySession, control *contracts.RunControl) *zenForgeExecutionState {
	return &zenForgeExecutionState{
		execCtx: &contracts.ExecutionContext{
			Request: req, Session: session, RunControl: control, Budget: session.ResolvedBudget,
			StageSettings: session.ResolvedStageSettings, ToolOverrides: cloneToolOverrides(session.ToolOverrides),
			RuntimeEnvOverrides: cloneZenForgeStringMap(session.RuntimeEnvOverrides), AccessLevel: session.AccessLevel,
			RunLoopState:          contracts.RunLoopStateIdle,
			AccessPolicyApprovals: map[string]int{}, AccessPolicyRuleApprovals: map[string]bool{},
			BashSecurityApprovals: map[string]int{},
			FileReadApprovals:     map[string]int{}, FileReadRuleApprovals: map[string]bool{},
			FileAccessApprovals: map[string]int{}, FileAccessRuleApprovals: map[string]bool{},
			FileWriteApprovals: map[string]int{}, FileWriteRuleApprovals: map[string]bool{},
			ReadFileState: map[string]contracts.ReadFileSnapshot{},
		},
		requests: map[string]zenForgeApprovalRecord{}, approvedRules: map[string]bool{},
	}
}

func newZenForgeTools(executor contracts.ToolExecutor, definitions []api.ToolDetailResponse, req api.QueryRequest, session contracts.QuerySession, control *contracts.RunControl) ([]tool.Tool, tool.Invoker, *zenForgeToolResultCache) {
	tools := make([]tool.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, zenForgeTool{definition: definition})
	}
	state := newZenForgeExecutionState(req, session, control)
	cache := &zenForgeToolResultCache{values: map[string]contracts.ToolExecutionResult{}}
	return tools, tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		args := map[string]any{}
		if len(call.Arguments) > 0 {
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				return tool.Result{Error: err.Error(), ExitCode: 1}, err
			}
		}
		state.execCtx.CurrentToolID, state.execCtx.CurrentToolName = call.ID, call.Name
		state.execCtx.RunLoopState = contracts.RunLoopStateToolExecuting
		state.applyApprovedMetadata(call.Metadata)
		result, err := executor.Invoke(ctx, call.Name, args, state.execCtx)
		if err != nil {
			cache.put(call.ID, result)
			return zenForgePlatformToolResult(result), err
		}
		record, approvalRequired := zenForgePlatformApproval(call, args, result)
		if approvalRequired {
			if record.RuleKey != "" && state.approvedRules[state.ruleID(record)] {
				state.registerApproval(record, true)
				result, err = executor.Invoke(ctx, call.Name, args, state.execCtx)
				if err != nil {
					cache.put(call.ID, result)
					return zenForgePlatformToolResult(result), err
				}
				if _, stillRequired := zenForgePlatformApproval(call, args, result); stillRequired {
					return tool.Result{Error: "platform approval remained required after rule authorization", ExitCode: 1}, fmt.Errorf("platform approval retry did not consume authorization")
				}
			} else {
				state.requests[record.Request.ID] = record
				return approval.RequiredResult(record.Request), nil
			}
		}
		cache.put(call.ID, result)
		return zenForgePlatformToolResult(result), nil
	}), cache
}

func (s *zenForgeExecutionState) applyApprovedMetadata(metadata map[string]any) {
	requestID := strings.TrimSpace(contracts.AnyStringNode(metadata[approval.MetadataRequestID]))
	if requestID == "" {
		return
	}
	record, ok := s.requests[requestID]
	if !ok {
		return
	}
	delete(s.requests, requestID)
	action := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(metadata[approval.MetadataDecisionAction])))
	rule := action == string(approval.DecisionAlways)
	s.registerApproval(record, rule)
	if rule && record.RuleKey != "" {
		s.approvedRules[s.ruleID(record)] = true
	}
}

func (s *zenForgeExecutionState) ruleID(record zenForgeApprovalRecord) string {
	return record.Kind + "\x00" + record.RuleKey
}

func (s *zenForgeExecutionState) registerApproval(record zenForgeApprovalRecord, rule bool) {
	if rule && record.RuleKey != "" {
		switch record.Kind {
		case "bash_access":
			accesspolicy.RegisterRuleApproval(s.execCtx, record.RuleKey)
		case "file_read", "vision_read":
			filetools.RegisterRuleReadApproval(s.execCtx, record.RuleKey)
		case "file_access":
			filetools.RegisterRuleAccessApproval(s.execCtx, record.RuleKey)
		case "file_write":
			filetools.RegisterRuleWriteApproval(s.execCtx, record.RuleKey)
		case "bash_security":
			if record.Fingerprint != "" {
				s.execCtx.BashSecurityApprovals[record.Fingerprint]++
			}
		}
		return
	}
	if record.Fingerprint == "" {
		return
	}
	switch record.Kind {
	case "bash_security":
		s.execCtx.BashSecurityApprovals[record.Fingerprint]++
	case "bash_access":
		accesspolicy.RegisterExactApproval(s.execCtx, record.Fingerprint)
	case "file_read", "vision_read":
		filetools.RegisterExactReadApproval(s.execCtx, record.Fingerprint)
	case "file_access":
		filetools.RegisterExactAccessApproval(s.execCtx, record.Fingerprint)
	case "file_write":
		filetools.RegisterExactWriteApproval(s.execCtx, record.Fingerprint)
	}
}

func zenForgePlatformApproval(call tool.Call, args map[string]any, result contracts.ToolExecutionResult) (zenForgeApprovalRecord, bool) {
	code := strings.TrimSpace(result.Error)
	if code == "" {
		code = strings.TrimSpace(contracts.AnyStringNode(result.Structured["error"]))
	}
	kind := map[string]string{
		"bash_security_approval_required":    "bash_security",
		"bash_access_approval_required":      "bash_access",
		"file_read_approval_required":        "file_read",
		"vision_recognize_approval_required": "vision_read",
		"file_write_path_approval_required":  "file_access",
		"file_edit_path_approval_required":   "file_access",
		"file_write_approval_required":       "file_write",
		"file_edit_approval_required":        "file_write",
	}[code]
	if kind == "" {
		return zenForgeApprovalRecord{}, false
	}
	fingerprint := strings.TrimSpace(contracts.AnyStringNode(result.Structured["fingerprint"]))
	ruleKey := strings.TrimSpace(contracts.AnyStringNode(result.Structured["ruleKey"]))
	command := firstNonBlank(contracts.AnyStringNode(result.Structured["command"]), contracts.AnyStringNode(args["command"]))
	path := firstNonBlank(contracts.AnyStringNode(result.Structured["filePath"]), contracts.AnyStringNode(result.Structured["path"]), contracts.AnyStringNode(args["file_path"]))
	message := firstNonBlank(contracts.AnyStringNode(result.Structured["message"]), result.Output, code)
	payload := map[string]any{
		"platformError": code, "fingerprint": fingerprint, "ruleKey": ruleKey,
		"command": command, "path": path, "message": message,
		"originalStructured": contracts.CloneAnyMap(result.Structured),
	}
	req := approval.Request{
		ID: approval.NewRequestID(call.RunID, call.ID, code), RunID: call.RunID, ToolCallID: call.ID, ToolName: call.Name,
		Operation: code, Title: zenForgeApprovalTitle(code, call.Name), Description: message, Risk: approval.RiskHigh,
		Options: []approval.Option{
			{Action: approval.DecisionApprove, Scope: approval.ScopeOnce, Label: "同意", Description: "只本次放行"},
			{Action: approval.DecisionAlways, Scope: approval.ScopeRule, Label: "同意（本次运行同规则都放行）", Description: "本次 run 内同规则自动放行"},
			{Action: approval.DecisionReject, Scope: approval.ScopeOnce, Label: "拒绝"},
		},
		Payload: payload, CreatedAt: time.Now().UTC(),
	}
	return zenForgeApprovalRecord{Request: req, Kind: kind, Fingerprint: fingerprint, RuleKey: ruleKey}, true
}

func zenForgeApprovalTitle(code, toolName string) string {
	switch {
	case strings.HasPrefix(code, "bash_"):
		return "执行命令"
	case strings.Contains(code, "read"), strings.HasPrefix(code, "vision_"):
		return "读取文件"
	case strings.Contains(code, "edit"):
		return "编辑文件"
	case strings.Contains(code, "write"):
		return "写入文件"
	default:
		return "批准 " + toolName
	}
}

func zenForgePlatformToolResult(result contracts.ToolExecutionResult) tool.Result {
	return tool.Result{
		Output: result.Output, Structured: contracts.CloneAnyMap(result.Structured), Error: result.Error, ExitCode: result.ExitCode,
		Metadata: map[string]any{"rawParams": result.RawParams, "hitl": contracts.CloneAnyMap(result.HITL)},
	}
}

func cloneZenForgeStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
