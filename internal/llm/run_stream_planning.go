package llm

import (
	"encoding/json"
	"strings"

	. "agent-platform/internal/contracts"
	planutil "agent-platform/internal/planning"
)

type planningWriteStreamState struct {
	toolID       string
	argsBuffer   string
	started      bool
	planningID   string
	planningFile string
	title        string
	sentMarkdown string
}

func (s *llmRunStream) appendToolCallDeltas(deltas []AgentDelta) {
	for _, delta := range deltas {
		s.pending = append(s.pending, delta)
		toolCall, ok := delta.(DeltaToolCall)
		if !ok {
			continue
		}
		s.pending = append(s.pending, s.planningDeltasFromToolCall(toolCall)...)
	}
}

func (s *llmRunStream) planningDeltasFromToolCall(delta DeltaToolCall) []AgentDelta {
	toolID := strings.TrimSpace(delta.ID)
	if toolID == "" {
		return nil
	}
	if !isPlanningWriteTool(delta.Name) {
		if s == nil || s.planningWrites == nil || s.planningWrites[toolID] == nil {
			return nil
		}
	}
	state := s.ensurePlanningWriteState(toolID)
	if state == nil {
		return nil
	}
	state.argsBuffer += delta.ArgsDelta
	args := partialPlanningWriteArgs(state.argsBuffer)
	title := strings.TrimSpace(AnyStringNode(args["title"]))
	if title == "" {
		return nil
	}
	state.title = title
	if state.planningID == "" {
		state.planningID = planutil.PlanningID(title, s.planningRunID())
	}
	if state.planningFile == "" {
		if chatsDir := s.planningChatsDir(); chatsDir != "" {
			state.planningFile = planutil.PlanningFile(chatsDir, state.planningID)
		}
	}
	events := make([]AgentDelta, 0)
	if !state.started {
		state.started = true
		events = append(events, s.planningStartDelta(state, "started"))
	}
	draft := planutil.RenderDraftMarkdown(args)
	events = append(events, s.planningMarkdownDeltas(state, draft)...)
	return events
}

func (s *llmRunStream) appendFinalPlanningDeltas(toolID string, result ToolExecutionResult) {
	planningID := strings.TrimSpace(AnyStringNode(result.Structured["planningId"]))
	planningFile := strings.TrimSpace(AnyStringNode(result.Structured["planningFile"]))
	title := strings.TrimSpace(AnyStringNode(result.Structured["title"]))
	status := strings.TrimSpace(AnyStringNode(result.Structured["status"]))
	markdown := AnyStringNode(result.Structured["markdown"])
	if status == "" {
		status = "ready"
	}
	if planningID == "" || strings.TrimSpace(markdown) == "" {
		return
	}
	state := s.ensurePlanningWriteState(toolID)
	if state == nil {
		state = &planningWriteStreamState{toolID: strings.TrimSpace(toolID)}
	}
	if state.planningID == "" {
		state.planningID = planningID
	}
	if state.planningFile == "" {
		state.planningFile = planningFile
	}
	if state.title == "" {
		state.title = title
	}
	if !state.started {
		state.started = true
		s.pending = append(s.pending, s.planningStartDelta(state, "started"))
	}
	s.pending = append(s.pending, s.planningMarkdownDeltas(state, markdown)...)
	s.pending = append(s.pending, DeltaPlanningEnd{
		PlanningID:   planningID,
		PlanningFile: planningFile,
		ChatID:       s.session.ChatID,
		RunID:        s.session.RunID,
		RequestID:    s.session.RequestID,
		AgentKey:     s.session.AgentKey,
		Title:        title,
		Status:       status,
		Markdown:     markdown,
	})
	if s.planningWrites != nil {
		delete(s.planningWrites, strings.TrimSpace(toolID))
	}
}

func (s *llmRunStream) planningMarkdownDeltas(state *planningWriteStreamState, markdown string) []AgentDelta {
	if state == nil || markdown == "" || markdown == state.sentMarkdown {
		return nil
	}
	if !strings.HasPrefix(markdown, state.sentMarkdown) {
		if state.sentMarkdown != "" {
			return nil
		}
	}
	suffix := markdown[len(state.sentMarkdown):]
	if suffix == "" {
		return nil
	}
	state.sentMarkdown += suffix
	chunks := splitPlanningDeltaChunks(suffix)
	events := make([]AgentDelta, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}
		events = append(events, DeltaPlanningDelta{
			PlanningID:   state.planningID,
			PlanningFile: state.planningFile,
			ChatID:       s.session.ChatID,
			RunID:        s.session.RunID,
			RequestID:    s.session.RequestID,
			AgentKey:     s.session.AgentKey,
			Title:        state.title,
			Status:       "writing",
			Delta:        chunk,
		})
	}
	return events
}

func splitPlanningDeltaChunks(text string) []string {
	if text == "" {
		return nil
	}
	chunks := make([]string, 0)
	start := 0
	for {
		idx := strings.Index(text[start:], "\n\n## ")
		if idx < 0 {
			break
		}
		boundary := start + idx + 2
		if boundary > start {
			chunks = append(chunks, text[start:boundary])
		}
		start = boundary
	}
	if start < len(text) {
		chunks = append(chunks, text[start:])
	}
	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}

func (s *llmRunStream) planningStartDelta(state *planningWriteStreamState, status string) DeltaPlanningStart {
	return DeltaPlanningStart{
		PlanningID:   state.planningID,
		PlanningFile: state.planningFile,
		ChatID:       s.session.ChatID,
		RunID:        s.session.RunID,
		RequestID:    s.session.RequestID,
		AgentKey:     s.session.AgentKey,
		Title:        state.title,
		Status:       status,
	}
}

func (s *llmRunStream) ensurePlanningWriteState(toolID string) *planningWriteStreamState {
	if s == nil {
		return nil
	}
	toolID = strings.TrimSpace(toolID)
	if toolID == "" {
		return nil
	}
	if s.planningWrites == nil {
		s.planningWrites = map[string]*planningWriteStreamState{}
	}
	state := s.planningWrites[toolID]
	if state == nil {
		state = &planningWriteStreamState{toolID: toolID}
		s.planningWrites[toolID] = state
	}
	return state
}

func (s *llmRunStream) planningRunID() string {
	if s == nil {
		return ""
	}
	runID := strings.TrimSpace(s.session.RunID)
	if runID == "" {
		runID = strings.TrimSpace(s.req.RunID)
	}
	if runID == "" {
		runID = strings.TrimSpace(s.session.RequestID)
	}
	return runID
}

func (s *llmRunStream) planningChatsDir() string {
	if s == nil {
		return ""
	}
	chatsDir := ""
	if s.engine != nil {
		chatsDir = strings.TrimSpace(s.engine.cfg.Paths.ChatsDir)
	}
	if chatsDir == "" {
		chatsDir = strings.TrimSpace(s.session.RuntimeContext.LocalPaths.ChatsDir)
	}
	return chatsDir
}

func partialPlanningWriteArgs(buffer string) map[string]any {
	var full map[string]any
	if err := json.Unmarshal([]byte(buffer), &full); err == nil && len(full) > 0 {
		return full
	}
	out := map[string]any{}
	for _, key := range []string{"title", "summary", "keyChanges", "steps", "testPlan", "assumptions"} {
		valueOffset := findJSONObjectValueOffset(buffer, key)
		if valueOffset < 0 || valueOffset >= len(buffer) {
			continue
		}
		switch buffer[valueOffset] {
		case '"':
			if value, _, ok := parseJSONStringAt(buffer, valueOffset); ok {
				out[key] = value
			}
		case '[':
			if values, closed := parsePartialJSONStringArray(buffer, valueOffset); closed {
				items := make([]any, 0, len(values))
				for _, value := range values {
					items = append(items, value)
				}
				out[key] = items
			}
		}
	}
	return out
}

func findJSONObjectValueOffset(text string, key string) int {
	for i := 0; i < len(text); i++ {
		if text[i] != '"' {
			continue
		}
		value, end, ok := parseJSONStringAt(text, i)
		if !ok {
			return -1
		}
		i = end
		if value != key {
			continue
		}
		j := skipJSONSpaces(text, end+1)
		if j >= len(text) || text[j] != ':' {
			continue
		}
		return skipJSONSpaces(text, j+1)
	}
	return -1
}

func parseJSONStringAt(text string, start int) (string, int, bool) {
	if start < 0 || start >= len(text) || text[start] != '"' {
		return "", start, false
	}
	escaped := false
	for i := start + 1; i < len(text); i++ {
		ch := text[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch != '"' {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(text[start:i+1]), &value); err != nil {
			return "", i, false
		}
		return value, i, true
	}
	return "", start, false
}

func parsePartialJSONStringArray(text string, start int) ([]string, bool) {
	if start < 0 || start >= len(text) || text[start] != '[' {
		return nil, false
	}
	items := make([]string, 0)
	for i := start + 1; i < len(text); {
		i = skipJSONSpaces(text, i)
		if i >= len(text) {
			return items, false
		}
		switch text[i] {
		case ']':
			return items, true
		case ',':
			i++
			continue
		case '"':
			value, end, ok := parseJSONStringAt(text, i)
			if !ok {
				return items, false
			}
			items = append(items, value)
			i = end + 1
		default:
			return items, false
		}
	}
	return items, false
}

func skipJSONSpaces(text string, start int) int {
	for start < len(text) {
		switch text[start] {
		case ' ', '\n', '\r', '\t':
			start++
		default:
			return start
		}
	}
	return start
}
