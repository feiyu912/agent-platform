package contracts

import "testing"

func TestStageSettingsParsesMaxOutputTokens(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"maxOutputTokens": 8192,
	})

	if settings.MaxOutputTokens != 8192 {
		t.Fatalf("expected maxOutputTokens 8192, got %d", settings.MaxOutputTokens)
	}
	if settings.IsZero() {
		t.Fatal("expected stage settings with maxOutputTokens to be non-zero")
	}
}

func TestStageSettingsIgnoresLegacyMaxTokens(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"maxTokens": 8192,
	})

	if settings.MaxOutputTokens != 0 {
		t.Fatalf("expected legacy maxTokens to be ignored, got %d", settings.MaxOutputTokens)
	}
	if !settings.IsZero() {
		t.Fatal("expected stage settings with only legacy maxTokens to be zero")
	}
}
