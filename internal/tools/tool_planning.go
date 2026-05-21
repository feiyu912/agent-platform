package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	. "agent-platform/internal/contracts"
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

	title := strings.TrimSpace(AnyStringNode(args["title"]))
	if title == "" {
		title = planningTitleFromRequest(execCtx.Request.Message)
	}
	if title == "" {
		title = "CODER Planning"
	}
	summary := strings.TrimSpace(AnyStringNode(args["summary"]))
	if summary == "" {
		summary = "Create and confirm an execution plan for the user request."
	}
	keyChanges := planningStringList(args, "keyChanges")
	steps := planningStringList(args, "steps")
	testPlan := planningStringList(args, "testPlan")
	assumptions := planningStringList(args, "assumptions")
	if len(steps) == 0 {
		return ToolExecutionResult{Output: "失败: steps 至少需要一项", Error: "missing_planning_steps", ExitCode: -1}, nil
	}

	runID := strings.TrimSpace(execCtx.Session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Request.RunID)
	}
	if runID == "" {
		runID = strings.TrimSpace(execCtx.Session.RequestID)
	}
	planningID := safePlanningFileStem(title) + "-" + safePlanningRunID(runID)
	planningFile := filepath.Join(chatsDir, "plans", planningID+".md")
	markdown := renderPlanningMarkdown(title, summary, keyChanges, steps, testPlan, assumptions)
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
		Title:        title,
		Markdown:     markdown,
		Status:       "ready",
		UpdatedAt:    updatedAt,
	}
	payload := map[string]any{
		"planningId":   planningID,
		"planningFile": planningFile,
		"title":        title,
		"status":       "ready",
		"markdown":     markdown,
		"updatedAt":    updatedAt,
	}
	result := structuredResultWithExit(payload, 0)
	result.Output = fmt.Sprintf("planning written: %s", planningFile)
	return result, nil
}

func planningTitleFromRequest(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	runes := []rune(message)
	if len(runes) > 40 {
		message = strings.TrimSpace(string(runes[:40]))
	}
	return message
}

func planningStringList(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case string:
		return splitPlanningLines(value)
	case []string:
		return cleanPlanningLines(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			switch typed := item.(type) {
			case string:
				out = append(out, typed)
			case map[string]any:
				out = append(out, planningMapListItem(typed))
			}
		}
		return cleanPlanningLines(out)
	default:
		return nil
	}
}

func splitPlanningLines(value string) []string {
	lines := strings.Split(value, "\n")
	return cleanPlanningLines(lines)
}

func cleanPlanningLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func planningMapListItem(item map[string]any) string {
	title := strings.TrimSpace(AnyStringNode(item["title"]))
	if title == "" {
		title = strings.TrimSpace(AnyStringNode(item["name"]))
	}
	description := strings.TrimSpace(AnyStringNode(item["description"]))
	if description == "" {
		description = strings.TrimSpace(AnyStringNode(item["text"]))
	}
	if title != "" && description != "" {
		return title + ": " + description
	}
	if title != "" {
		return title
	}
	return description
}

func renderPlanningMarkdown(title string, summary string, keyChanges []string, steps []string, testPlan []string, assumptions []string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(strings.TrimSpace(title))
	b.WriteString("\n\n## Summary\n")
	b.WriteString(strings.TrimSpace(summary))
	b.WriteString("\n\n")
	writePlanningSection(&b, "Key Changes", keyChanges)
	writePlanningSection(&b, "Plan", steps)
	writePlanningSection(&b, "Test Plan", testPlan)
	writePlanningSection(&b, "Assumptions", assumptions)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writePlanningSection(b *strings.Builder, title string, lines []string) {
	b.WriteString("## ")
	b.WriteString(title)
	b.WriteByte('\n')
	if len(lines) == 0 {
		b.WriteString("- None specified.\n\n")
		return
	}
	for _, line := range lines {
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(line))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func safePlanningFileStem(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "planning"
	}
	var b strings.Builder
	lastDash := false
	count := 0
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			count++
		} else if r == '_' {
			b.WriteRune(r)
			lastDash = false
			count++
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
		if count >= 80 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "planning"
	}
	return out
}

func safePlanningRunID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "run"
	}
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "run"
	}
	return out
}
