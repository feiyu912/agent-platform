package config

import (
	"strings"
	"testing"
)

func TestZenForgeDefaultsAndYAML(t *testing.T) {
	cfg := defaultConfig()
	if cfg.ZenForge.Enabled || !cfg.ZenForge.FallbackOnInitError {
		t.Fatalf("unexpected defaults: %#v", cfg.ZenForge)
	}
	err := cfg.applyZenForgeValues("runtime.yml", map[string]any{
		"enabled":                true,
		"fallback-on-init-error": false,
		"agent-overrides": map[string]any{
			"agent-a": "zenforge",
		},
		"chat-overrides": map[string]any{
			"chat-a": "legacy",
		},
	})
	if err != nil {
		t.Fatalf("apply YAML: %v", err)
	}
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !cfg.ZenForge.Enabled || cfg.ZenForge.FallbackOnInitError {
		t.Fatalf("unexpected YAML booleans: %#v", cfg.ZenForge)
	}
	if cfg.ZenForge.AgentOverrides["agent-a"] != "zenforge" || cfg.ZenForge.ChatOverrides["chat-a"] != "legacy" {
		t.Fatalf("unexpected YAML overrides: %#v", cfg.ZenForge)
	}
}

func TestZenForgeOverrideNormalizationCopiesMaps(t *testing.T) {
	source := map[string]string{" agent-a ": " ZenForge "}
	cfg := defaultConfig()
	cfg.ZenForge.AgentOverrides = source
	if err := cfg.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	source["agent-a"] = "legacy"
	if got := cfg.ZenForge.AgentOverrides["agent-a"]; got != "zenforge" {
		t.Fatalf("normalized map was not independently owned: %q", got)
	}
}

func TestZenForgeInvalidOverridesFailClosed(t *testing.T) {
	tests := []struct {
		name string
		mapv map[string]string
	}{
		{name: "empty key", mapv: map[string]string{" ": "legacy"}},
		{name: "bad value", mapv: map[string]string{"agent": "other"}},
		{name: "normalized duplicate", mapv: map[string]string{"agent": "legacy", " agent ": "zenforge"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig()
			cfg.ZenForge.AgentOverrides = test.mapv
			if err := cfg.normalize(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestZenForgeEnvParsing(t *testing.T) {
	withIsolatedEnv(t, map[string]string{
		"ZENFORGE_ENABLED":                "true",
		"ZENFORGE_FALLBACK_ON_INIT_ERROR": "false",
		"ZENFORGE_AGENT_OVERRIDES":        "agent-a=zenforge,agent-b=legacy",
		"ZENFORGE_CHAT_OVERRIDES":         "chat-a=zenforge",
		"ZENFORGE_RUN_OVERRIDES":          "run-a=legacy",
	}, func() {
		cfg := defaultConfig()
		if err := cfg.applyZenForgeEnv(); err != nil {
			t.Fatalf("apply env: %v", err)
		}
		if !cfg.ZenForge.Enabled || cfg.ZenForge.FallbackOnInitError {
			t.Fatalf("unexpected booleans: %#v", cfg.ZenForge)
		}
		if len(cfg.ZenForge.AgentOverrides) != 2 || cfg.ZenForge.ChatOverrides["chat-a"] != "zenforge" || cfg.ZenForge.RunOverrides["run-a"] != "legacy" {
			t.Fatalf("unexpected env maps: %#v", cfg.ZenForge)
		}
	})
}

func TestZenForgeInvalidEnvFailsClosed(t *testing.T) {
	values := []string{"", "agent", "=legacy", "agent=", "agent=other", "agent=legacy,", "agent=legacy,agent=zenforge"}
	for _, value := range values {
		t.Run(strings.ReplaceAll(value, "=", "_"), func(t *testing.T) {
			withIsolatedEnv(t, map[string]string{"ZENFORGE_AGENT_OVERRIDES": value}, func() {
				if _, err := Load(); err == nil {
					t.Fatalf("expected %q to fail", value)
				}
			})
		})
	}
	withIsolatedEnv(t, map[string]string{"ZENFORGE_ENABLED": "sometimes"}, func() {
		if _, err := Load(); err == nil {
			t.Fatal("expected invalid boolean to fail")
		}
	})
}

func TestZenForgeInvalidYAMLTypesFailClosed(t *testing.T) {
	cfg := defaultConfig()
	if err := cfg.applyZenForgeValues("runtime.yml", map[string]any{"enabled": "yes"}); err == nil {
		t.Fatal("expected invalid YAML boolean")
	}
	if err := cfg.applyZenForgeValues("runtime.yml", map[string]any{"agent-overrides": []any{"agent=legacy"}}); err == nil {
		t.Fatal("expected invalid YAML map")
	}
}
