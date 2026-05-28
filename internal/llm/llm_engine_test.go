package llm

import (
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestResolveMaxStepsUsesBudgetAndLegacyReactFallback(t *testing.T) {
	engine := &LLMAgentEngine{
		cfg: config.Config{
			Defaults: config.DefaultsConfig{
				React: config.ReactDefaultsConfig{MaxSteps: 6},
			},
		},
	}

	if got := engine.resolveMaxSteps(contracts.QuerySession{ReactMaxSteps: 160}, "react"); got != 160 {
		t.Fatalf("resolveMaxSteps() = %d, want legacy react override 160", got)
	}
	if got := engine.resolveMaxSteps(contracts.QuerySession{
		Budget:        map[string]any{"maxSteps": 24},
		ReactMaxSteps: 160,
		ResolvedBudget: contracts.Budget{
			MaxSteps: 24,
		},
	}, "react"); got != 24 {
		t.Fatalf("resolveMaxSteps() = %d, want budget max steps 24", got)
	}
	if got := engine.resolveMaxSteps(contracts.QuerySession{}, "react"); got != 100 {
		t.Fatalf("resolveMaxSteps() = %d, want budget default 100", got)
	}
}
