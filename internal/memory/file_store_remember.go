package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

func (s *FileStore) Remember(chatDetail chat.Detail, request api.RememberRequest, agentKey string) (api.RememberResponse, error) {
	history, err := s.readAllStored()
	if err != nil {
		return api.RememberResponse{}, err
	}
	drafts := summarizeRememberWithFallback(s.summarizer, RememberSynthesisInput{
		Request:  request,
		Chat:     chatDetail,
		AgentKey: agentKey,
		History:  filterHistoryByAgent(history, agentKey),
	})
	stored := buildRememberStoredItems(request, chatDetail, agentKey, drafts)
	for _, item := range stored {
		if err := s.Write(item); err != nil {
			return api.RememberResponse{}, err
		}
	}
	logMemoryOperation("remember", map[string]any{
		"agentKey":    agentKey,
		"chatId":      request.ChatID,
		"requestId":   request.RequestID,
		"memoryCount": len(stored),
	})

	memoryPath := filepath.Join(s.root, request.ChatID+".json")
	items := make([]api.RememberItemResponse, 0, len(stored))
	for _, item := range stored {
		items = append(items, api.RememberItemResponse{
			Summary:    item.Summary,
			SubjectKey: chatDetail.ChatID,
		})
	}
	payload := map[string]any{
		"requestId": request.RequestID,
		"chatId":    request.ChatID,
		"chatName":  chatDetail.ChatName,
		"items":     items,
		"stored":    stored,
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return api.RememberResponse{}, err
	}
	if err := os.WriteFile(memoryPath, data, 0o644); err != nil {
		return api.RememberResponse{}, err
	}

	preview := &api.PromptPreviewResponse{
		UserPrompt:        firstRawMessage(chatDetail.RawMessages),
		ChatName:          chatDetail.ChatName,
		RawMessageCount:   len(chatDetail.RawMessages),
		EventCount:        len(chatDetail.Events),
		ReferenceCount:    len(chatDetail.References),
		RawMessageSamples: sampleMessages(chatDetail.RawMessages),
		EventSamples:      sampleEvents(chatDetail.Events),
	}

	return api.RememberResponse{
		Accepted:      len(stored) > 0,
		Status:        rememberStatus(stored),
		RequestID:     request.RequestID,
		ChatID:        request.ChatID,
		MemoryPath:    memoryPath,
		MemoryRoot:    s.root,
		MemoryCount:   len(stored),
		Detail:        "remember request captured; memory root=" + s.root,
		PromptPreview: preview,
		Items:         items,
		Stored:        stored,
	}, nil
}

func extractRememberSummary(detail chat.Detail) string {
	for i := len(detail.RawMessages) - 1; i >= 0; i-- {
		message := detail.RawMessages[i]
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		if role == "assistant" && strings.TrimSpace(content) != "" {
			return content
		}
	}
	if len(detail.Events) > 0 {
		last := detail.Events[len(detail.Events)-1]
		if text := last.String("text"); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return "No assistant memory extracted yet."
}

func firstRawMessage(raw []map[string]any) string {
	for _, message := range raw {
		if content, _ := message["content"].(string); strings.TrimSpace(content) != "" {
			return content
		}
	}
	return ""
}

func sampleMessages(raw []map[string]any) []string {
	samples := make([]string, 0, min(3, len(raw)))
	for _, message := range raw {
		role, _ := message["role"].(string)
		content, _ := message["content"].(string)
		if strings.TrimSpace(content) == "" {
			continue
		}
		samples = append(samples, role+": "+content)
		if len(samples) == 3 {
			return samples
		}
	}
	return samples
}

func sampleEvents(events []stream.EventData) []string {
	samples := make([]string, 0, min(3, len(events)))
	for _, event := range events {
		samples = append(samples, event.Type)
		if len(samples) == 3 {
			return samples
		}
	}
	return samples
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
