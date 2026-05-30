package chat

import (
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/stream"
)

func planningSnapshotFromLine(line map[string]any, chatDir string) (*PlanningState, *stream.EventData) {
	event, _ := line["event"].(map[string]any)
	if len(event) == 0 || strings.TrimSpace(stringFromAny(event["type"])) != "planning.snapshot" {
		return nil, nil
	}

	planningID := strings.TrimSpace(stringFromAny(event["planningId"]))
	planningFile := strings.TrimSpace(stringFromAny(event["planningFile"]))
	if planningID == "" && planningFile != "" {
		base := filepath.Base(planningFile)
		planningID = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if planningID == "" || planningFile == "" {
		return nil, nil
	}

	resolvedFile := resolvePlanningFileForReplay(planningFile, chatDir, planningID)
	responseFile := planningFile
	if fileExists(resolvedFile) || responseFile == "" {
		responseFile = resolvedFile
	}

	markdown := stringFromAny(event["text"])
	if markdown == "" {
		markdown = stringFromAny(event["markdown"])
	}
	if markdown == "" && resolvedFile != "" {
		if data, err := os.ReadFile(resolvedFile); err == nil {
			markdown = string(data)
		}
	}

	timestamp := int64FromAny(event["timestamp"])
	if timestamp == 0 {
		timestamp = int64FromAny(line["updatedAt"])
	}
	chatID := strings.TrimSpace(stringFromAny(event["chatId"]))
	if chatID == "" {
		chatID = strings.TrimSpace(stringFromAny(line["chatId"]))
	}
	runID := strings.TrimSpace(stringFromAny(event["runId"]))
	if runID == "" {
		runID = strings.TrimSpace(stringFromAny(line["runId"]))
	}

	payload := map[string]any{
		"planningId":   planningID,
		"planningFile": responseFile,
	}
	if chatID != "" {
		payload["chatId"] = chatID
	}
	if runID != "" {
		payload["runId"] = runID
	}
	if markdown != "" {
		payload["text"] = markdown
	}

	state := &PlanningState{
		PlanningID:   planningID,
		PlanningFile: responseFile,
		Markdown:     markdown,
	}
	eventData := &stream.EventData{
		Type:      "planning.snapshot",
		Timestamp: timestamp,
		Payload:   payload,
	}
	return state, eventData
}

func resolvePlanningFileForReplay(planningFile string, chatDir string, planningID string) string {
	planningFile = strings.TrimSpace(planningFile)
	chatDir = strings.TrimSpace(chatDir)
	planningID = strings.TrimSpace(planningID)

	candidates := make([]string, 0, 2)
	if planningFile != "" {
		candidates = append(candidates, planningFile)
	}
	if chatDir != "" && planningID != "" {
		candidates = append(candidates, filepath.Join(chatDir, ToolRootDirName, ToolPlansDirName, planningID+".md"))
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
