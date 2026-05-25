package server

import (
	"testing"

	"agent-platform/internal/stream"
)

func TestLatestChatUsageFromEventsReadsHistoricalUsageSnapshot(t *testing.T) {
	usage := latestChatUsageFromEvents([]stream.EventData{
		{
			Type: "usage.snapshot",
			Payload: map[string]any{
				"usage": map[string]any{
					"current": map[string]any{
						"promptTokens":           100,
						"completionTokens":       50,
						"totalTokens":            150,
						"llmChatCompletionCount": 1,
					},
					"chat": map[string]any{
						"promptTokens":     6574,
						"completionTokens": 104,
						"totalTokens":      6678,
						"completionTokensDetails": map[string]any{
							"reasoningTokens": 70,
						},
						"llmChatCompletionCount": 1,
					},
				},
			},
		},
	})
	if usage == nil || usage.PromptTokens != 6574 || usage.CompletionTokens != 104 || usage.TotalTokens != 6678 {
		t.Fatalf("expected chat cumulative usage, got %#v", usage)
	}
	if usage.CompletionTokensDetails == nil || usage.CompletionTokensDetails.ReasoningTokens != 70 ||
		usage.LlmChatCompletionCount != 1 {
		t.Fatalf("expected detailed chat cumulative usage, got %#v", usage)
	}
}
