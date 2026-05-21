package stream

import (
	"strings"
	"time"
)

func (d *StreamEventDispatcher) handlePlanningStart(input PlanningStart) []StreamEvent {
	events := d.closeOpenBlocks()
	payload := d.planningPayload(input.PlanningID, input.PlanningFile, input.ChatID, input.RunID, input.RequestID, input.AgentKey, input.Title, statusOrDefault(input.Status, "started"))
	events = append(events, NewEvent("planning.start", payload))
	return events
}

func (d *StreamEventDispatcher) handlePlanningDelta(input PlanningDelta) []StreamEvent {
	payload := d.planningPayload(input.PlanningID, input.PlanningFile, input.ChatID, input.RunID, input.RequestID, input.AgentKey, input.Title, statusOrDefault(input.Status, "writing"))
	payload["delta"] = input.Delta
	return []StreamEvent{NewEvent("planning.delta", payload)}
}

func (d *StreamEventDispatcher) handlePlanningSnapshot(input PlanningSnapshot) []StreamEvent {
	payload := d.planningPayload(input.PlanningID, input.PlanningFile, input.ChatID, input.RunID, input.RequestID, input.AgentKey, input.Title, statusOrDefault(input.Status, "ready"))
	payload["markdown"] = input.Markdown
	return []StreamEvent{NewEvent("planning.snapshot", payload)}
}

func (d *StreamEventDispatcher) handlePlanningEnd(input PlanningEnd) []StreamEvent {
	payload := d.planningPayload(input.PlanningID, input.PlanningFile, input.ChatID, input.RunID, input.RequestID, input.AgentKey, input.Title, statusOrDefault(input.Status, "ready"))
	if strings.TrimSpace(input.Markdown) != "" {
		payload["markdown"] = input.Markdown
	}
	return []StreamEvent{NewEvent("planning.end", payload)}
}

func (d *StreamEventDispatcher) planningPayload(planningID string, planningFile string, chatID string, runID string, requestID string, agentKey string, title string, status string) map[string]any {
	if strings.TrimSpace(chatID) == "" {
		chatID = d.request.ChatID
	}
	if strings.TrimSpace(runID) == "" {
		runID = d.request.RunID
	}
	if strings.TrimSpace(requestID) == "" {
		requestID = d.request.RequestID
	}
	if strings.TrimSpace(agentKey) == "" {
		agentKey = d.request.AgentKey
	}
	return map[string]any{
		"planningId":   strings.TrimSpace(planningID),
		"planningFile": strings.TrimSpace(planningFile),
		"chatId":       strings.TrimSpace(chatID),
		"runId":        strings.TrimSpace(runID),
		"requestId":    strings.TrimSpace(requestID),
		"agentKey":     strings.TrimSpace(agentKey),
		"title":        strings.TrimSpace(title),
		"status":       strings.TrimSpace(status),
		"updatedAt":    time.Now().UnixMilli(),
	}
}

func statusOrDefault(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
