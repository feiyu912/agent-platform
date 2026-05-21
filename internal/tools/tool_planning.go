package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

func (t *RuntimeToolExecutor) invokePlanningWrite(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if execCtx == nil {
		return ToolExecutionResult{Output: "失败: 缺少执行上下文", Error: "planning_context_unavailable", ExitCode: -1}, nil
	}
	if !execCtx.Session.PlanningMode {
		return ToolExecutionResult{Output: "失败: planning_write 只能在 CODER planningMode 阶段使用", Error: "planning_write_not_allowed", ExitCode: -1}, nil
	}
	chatsDir := strings.TrimSpace(t.cfg.Paths.ChatsDir)
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatsDir)
	}
	if chatsDir == "" {
		return ToolExecutionResult{Output: "失败: CHATS_DIR 未配置", Error: "chats_dir_unavailable", ExitCode: -1}, nil
	}

	spec := planutil.SpecFromArgs(args, execCtx.Request.Message)
	if len(spec.Steps) == 0 {
		return ToolExecutionResult{Output: "失败: steps 至少需要一项", Error: "missing_planning_steps", ExitCode: -1}, nil
	}

	planningID := planutil.PlanningID(spec.Title, planningRunID(execCtx))
	planningFile := planutil.PlanningFile(chatsDir, planningID)
	markdown := planutil.RenderMarkdown(spec)
	if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
		return ToolExecutionResult{Output: "失败: 创建 plans 目录失败: " + err.Error(), Error: "planning_write_failed", ExitCode: -1}, nil
	}
	if err := os.WriteFile(planningFile, []byte(markdown), 0o644); err != nil {
		return ToolExecutionResult{Output: "失败: 写入 planning markdown 失败: " + err.Error(), Error: "planning_write_failed", ExitCode: -1}, nil
	}

	updatedAt := time.Now().UnixMilli()
	execCtx.PlanningState = &PlanningRuntimeState{
		PlanningID:   planningID,
		PlanningFile: planningFile,
		Title:        spec.Title,
		Markdown:     markdown,
		Status:       "ready",
		UpdatedAt:    updatedAt,
	}
	payload := map[string]any{
		"planningId":   planningID,
		"planningFile": planningFile,
		"title":        spec.Title,
		"status":       "ready",
		"markdown":     markdown,
		"updatedAt":    updatedAt,
	}
	result := structuredResultWithExit(payload, 0)
	result.Output = fmt.Sprintf("planning written: %s", planningFile)
	return result, nil
}

func planningRunID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	runID := strings.TrimSpace(execCtx.Session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Request.RunID)
	}
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Session.RequestID)
	}
	return runID
}
