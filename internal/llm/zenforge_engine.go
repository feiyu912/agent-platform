package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"github.com/feiyu912/zenforge"
	zenmind "github.com/feiyu912/zenforge/adapters/zenmind"
	"github.com/feiyu912/zenforge/checkpoint"
	checkpointjsonl "github.com/feiyu912/zenforge/checkpoint/jsonl"
	eventjsonl "github.com/feiyu912/zenforge/eventlog/jsonl"
	"github.com/feiyu912/zenforge/model"
)

type ZenForgeEngineConfig struct {
	Models         *models.ModelRegistry
	Tools          contracts.ToolExecutor
	HTTPClient     *http.Client
	Checkpoints    checkpoint.Store
	Events         zenforge.EventStore
	PersistenceDir string
}

type ZenForgeAgentEngine struct {
	models      *models.ModelRegistry
	tools       contracts.ToolExecutor
	httpClient  *http.Client
	checkpoints checkpoint.Store
	events      zenforge.EventStore
}

var _ contracts.AgentEngine = (*ZenForgeAgentEngine)(nil)

func NewZenForgeAgentEngine(cfg ZenForgeEngineConfig) (*ZenForgeAgentEngine, error) {
	if cfg.Models == nil {
		return nil, fmt.Errorf("zenforge model registry is required")
	}
	if cfg.Tools == nil {
		return nil, fmt.Errorf("zenforge tool executor is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.Checkpoints == nil || cfg.Events == nil {
		root := strings.TrimSpace(cfg.PersistenceDir)
		if root == "" {
			return nil, fmt.Errorf("zenforge persistence directory or stores are required")
		}
		if cfg.Checkpoints == nil {
			cfg.Checkpoints = checkpointjsonl.New(filepath.Join(root, "checkpoints"))
		}
		if cfg.Events == nil {
			cfg.Events = eventjsonl.New(filepath.Join(root, "events"))
		}
	}
	return &ZenForgeAgentEngine{
		models: cfg.Models, tools: cfg.Tools, httpClient: cfg.HTTPClient,
		checkpoints: cfg.Checkpoints, events: cfg.Events,
	}, nil
}

func (e *ZenForgeAgentEngine) Stream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession) (contracts.AgentStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runID := firstNonBlank(session.RunID, req.RunID)
	if runID == "" {
		return nil, fmt.Errorf("zenforge run id is required")
	}
	if err := validateZenForgeIdentity(req, session); err != nil {
		return nil, err
	}
	zenMode, err := zenForgeMode(session)
	if err != nil {
		return nil, err
	}
	control := contracts.RunControlFromContext(ctx)
	if control == nil {
		control = contracts.NewRunControl(ctx, runID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	stopControlLink := context.AfterFunc(control.Context(), cancel)
	combinedCancel := func() {
		stopControlLink()
		cancel()
	}
	approvals := newZenForgeApprovalBridge(control)
	definitions := applyToolOverrides(filterToolDefinitions(e.tools.Definitions(), session.ToolNames), session.ToolOverrides)
	bridgeTools, invoker, toolResults := newZenForgeTools(e.tools, definitions, req, session, control)
	usage := &zenForgeUsageTracker{}
	prompt := zenForgeResolvedPrompt(session, req, definitions, zenMode)
	agentDTO := zenmind.CatalogAgent{
		Key: session.AgentKey, Name: session.AgentName, ModelKey: session.ModelKey, Mode: zenMode,
		Tools: append([]string(nil), session.ToolNames...), Skills: append([]string(nil), session.SkillKeys...),
		ContextTags: append([]string(nil), session.ContextTags...), Budget: contracts.CloneAnyMap(session.Budget),
		ReactMaxSteps: session.ReactMaxSteps, Workspace: zenmind.Workspace{Root: session.WorkspaceRoot},
		HostAccess: zenmind.HostAccess{
			ReadRoots:  append([]string(nil), session.RuntimeHostAccess.ReadRoots...),
			WriteRoots: append([]string(nil), session.RuntimeHostAccess.WriteRoots...),
		},
	}
	sessionDTO := zenmind.Session{
		RequestID: firstNonBlank(req.RequestID, session.RequestID), ChatID: firstNonBlank(req.ChatID, session.ChatID),
		RunID: runID, AgentKey: firstNonBlank(req.AgentKey, session.AgentKey), ModelKey: zenForgeModelKey(req, session),
		Mode: zenMode, PlanningMode: session.PlanningMode, AccessLevel: firstNonBlank(req.AccessLevel, session.AccessLevel),
		HistoryMessages: cloneZenForgeHistory(session.HistoryMessages), ResolvedPrompt: prompt,
		WorkspaceRoot: session.WorkspaceRoot, Message: req.Message, TeamID: firstNonBlank(req.TeamID, session.TeamID),
		Metadata: map[string]any{
			"references": req.References, "params": contracts.CloneAnyMap(req.Params),
			"runtimeEnvironmentId": session.RuntimeEnvironmentID,
			"stagePrompts":         map[string]any{"plan": session.PlanPrompt, "execute": session.ExecutePrompt, "summary": session.SummaryPrompt},
		},
	}
	runtime := zenmind.Runtime{
		ModelResolver: zenmind.ModelResolverFunc(func(ctx context.Context, key string) (model.Model, error) {
			resolved, resolveErr := e.resolveZenForgeModel(ctx, key)
			if resolveErr != nil {
				return nil, resolveErr
			}
			return zenForgeTrackingModel{next: resolved, usage: usage}, nil
		}), Tools: bridgeTools, ToolInvoker: invoker,
		Approval: approvals, Checkpoints: e.checkpoints, Events: e.events,
	}
	run, err := zenmind.BuildRun(runCtx, agentDTO, sessionDTO, runtime)
	if err != nil {
		combinedCancel()
		return nil, err
	}
	agent := zenforge.New(run.Config)
	var events <-chan zenforge.Event
	if _, loadErr := e.checkpoints.Load(runCtx, runID); loadErr == nil {
		events, err = agent.Resume(runCtx, runID)
	} else if errors.Is(loadErr, checkpoint.ErrNotFound) {
		events, err = agent.Stream(runCtx, run.Task)
	} else {
		err = fmt.Errorf("load zenforge checkpoint %q: %w", runID, loadErr)
	}
	if err != nil {
		combinedCancel()
		return nil, err
	}
	return newZenForgeAgentStream(runCtx, combinedCancel, events, control, approvals, toolResults, usage, firstNonBlank(req.RequestID, session.RequestID), session), nil
}

func validateZenForgeIdentity(req api.QueryRequest, session contracts.QuerySession) error {
	for _, item := range []struct {
		name, request, session string
	}{
		{"run id", req.RunID, session.RunID},
		{"chat id", req.ChatID, session.ChatID},
		{"agent key", req.AgentKey, session.AgentKey},
	} {
		if strings.TrimSpace(item.request) != "" && strings.TrimSpace(item.session) != "" &&
			strings.TrimSpace(item.request) != strings.TrimSpace(item.session) {
			return fmt.Errorf("zenforge %s mismatch between request and session", item.name)
		}
	}
	return nil
}

func zenForgeMode(session contracts.QuerySession) (string, error) {
	mode := strings.ToUpper(strings.TrimSpace(session.Mode))
	switch mode {
	case "", "REACT", "ONESHOT", "PLAN_EXECUTE", "PLAN-EXECUTE":
		return mode, nil
	case "CODER":
		if session.PlanningMode {
			return "PLAN_EXECUTE", nil
		}
		return "REACT", nil
	default:
		return "", fmt.Errorf("unsupported zenforge agent mode %q", session.Mode)
	}
}

func zenForgeResolvedPrompt(session contracts.QuerySession, req api.QueryRequest, definitions []api.ToolDetailResponse, zenMode string) string {
	stage := strings.ToLower(strings.TrimSpace(session.Mode))
	if zenMode == "PLAN_EXECUTE" {
		stage = ""
	}
	base := buildSystemPrompt(session, req, "", PromptBuildOptions{Stage: stage, ToolDefinitions: definitions, IncludeAfterCallHints: true})
	if zenMode != "PLAN_EXECUTE" {
		return base
	}
	return joinPromptSections(
		base,
		zenForgePromptSection("Plan stage instructions", session.PlanPrompt),
		zenForgePromptSection("Execute stage instructions", session.ExecutePrompt),
		zenForgePromptSection("Summary stage instructions", session.SummaryPrompt),
	)
}

func zenForgePromptSection(label, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return label + "\n" + value
}

func zenForgeModelKey(req api.QueryRequest, session contracts.QuerySession) string {
	if req.Model != nil && strings.TrimSpace(req.Model.Key) != "" {
		return strings.TrimSpace(req.Model.Key)
	}
	return strings.TrimSpace(session.ModelKey)
}

func cloneZenForgeHistory(in []map[string]any) []map[string]any {
	out := make([]map[string]any, len(in))
	for i := range in {
		out[i] = contracts.CloneAnyMap(in[i])
	}
	return out
}
