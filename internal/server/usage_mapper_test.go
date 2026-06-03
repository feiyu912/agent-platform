package server

import (
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
)

func TestUsageCacheTokensKeepsConsistentDetails(t *testing.T) {
	hit, miss := usageCacheTokens(chat.UsageData{
		PromptTokens:          100,
		PromptCacheHitTokens:  40,
		PromptCacheMissTokens: 60,
	})

	if hit != 40 || miss != 60 {
		t.Fatalf("expected consistent cache details to remain unchanged, got hit=%d miss=%d", hit, miss)
	}
}

func TestUsageCacheTokensDerivesMissingCacheMissTokens(t *testing.T) {
	hit, miss := usageCacheTokens(chat.UsageData{
		PromptTokens:         100,
		PromptCacheHitTokens: 40,
	})

	if hit != 40 || miss != 60 {
		t.Fatalf("expected missing cache miss to derive from prompt minus hit, got hit=%d miss=%d", hit, miss)
	}
}

func TestUsageCacheTokensRecomputesInconsistentCacheMissTokens(t *testing.T) {
	hit, miss := usageCacheTokensFromMap(map[string]any{
		"promptTokens": 16929,
		"promptTokensDetails": map[string]any{
			"cacheHitTokens":  8059,
			"cacheMissTokens": 692,
		},
	})

	if hit != 8059 || miss != 8870 {
		t.Fatalf("expected inconsistent cache miss to derive from prompt minus hit, got hit=%d miss=%d", hit, miss)
	}
}

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
						"toolCallCount":          3,
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
	if usage.ToolCallCount != 3 {
		t.Fatalf("expected chat tool call count, got %#v", usage)
	}
}

func TestChatUsageBreakdownPrefersLatestRunAndHistoricalChatUsage(t *testing.T) {
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 111, CompletionTokens: 22, TotalTokens: 133, LlmChatCompletionCount: 2, ToolCallCount: 4},
		[]chat.RunSummary{
			{RunID: "run-2", Usage: chat.UsageData{ModelKey: "mock-model", PromptTokens: 11, CompletionTokens: 5, TotalTokens: 16, ReasoningTokens: 3, EstimatedCostCurrency: "CNY", EstimatedCostTotal: 0.12, LlmChatCompletionCount: 1, ToolCallCount: 2}},
			{RunID: "run-1", Usage: chat.UsageData{PromptTokens: 100, CompletionTokens: 17, TotalTokens: 117, LlmChatCompletionCount: 1}},
		},
		[]stream.EventData{
			{
				Type: "usage.snapshot",
				Payload: map[string]any{
					"usage": map[string]any{
						"run": map[string]any{
							"promptTokens":           11,
							"completionTokens":       5,
							"totalTokens":            16,
							"llmChatCompletionCount": 1,
						},
						"chat": map[string]any{
							"promptTokens":           111,
							"completionTokens":       22,
							"totalTokens":            133,
							"llmChatCompletionCount": 2,
						},
					},
				},
			},
		},
	)
	if breakdown == nil || breakdown.LastRun == nil || breakdown.Chat == nil {
		t.Fatalf("expected usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun.PromptTokens != 11 || breakdown.LastRun.CompletionTokens != 5 || breakdown.LastRun.TotalTokens != 16 {
		t.Fatalf("expected latest run usage, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.CompletionTokensDetails == nil || breakdown.LastRun.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("expected latest run usage from run summary, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.ToolCallCount != 2 {
		t.Fatalf("expected latest run tool call count, got %#v", breakdown.LastRun)
	}
	if breakdown.LastRun.ModelKey != "mock-model" || breakdown.LastRun.EstimatedCost == nil || breakdown.LastRun.EstimatedCost.Total != 0.12 {
		t.Fatalf("expected latest run modelKey and cost, got %#v", breakdown.LastRun)
	}
	if breakdown.Chat.PromptTokens != 111 || breakdown.Chat.CompletionTokens != 22 || breakdown.Chat.TotalTokens != 133 ||
		breakdown.Chat.LlmChatCompletionCount != 2 || breakdown.Chat.ToolCallCount != 4 {
		t.Fatalf("expected chat cumulative usage, got %#v", breakdown.Chat)
	}
}

func TestChatUsageBreakdownUsesSummaryChatUsageWithoutHistoricalRunFallback(t *testing.T) {
	breakdown := chatUsageBreakdown(
		&chat.UsageData{PromptTokens: 30, CompletionTokens: 7, TotalTokens: 37, LlmChatCompletionCount: 2},
		nil,
		[]stream.EventData{
			{
				Type: "run.complete",
				Payload: map[string]any{
					"usage": map[string]any{
						"run": map[string]any{
							"promptTokens":           3,
							"completionTokens":       4,
							"totalTokens":            7,
							"llmChatCompletionCount": 1,
						},
					},
				},
			},
		},
	)
	if breakdown == nil || breakdown.Chat == nil {
		t.Fatalf("expected fallback usage breakdown, got %#v", breakdown)
	}
	if breakdown.LastRun != nil {
		t.Fatalf("did not expect last run fallback from events, got %#v", breakdown.LastRun)
	}
	if breakdown.Chat.TotalTokens != 37 || breakdown.Chat.LlmChatCompletionCount != 2 {
		t.Fatalf("expected chat fallback from summary, got %#v", breakdown.Chat)
	}
}
