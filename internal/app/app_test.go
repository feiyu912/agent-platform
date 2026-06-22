package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/engine"
	"agent-platform/internal/models"
)

type blockingAutomation struct {
	done context.Context
}

func (s blockingAutomation) Stop() context.Context {
	return s.done
}

func TestAppCloseReturnsWhenAutomationStopTimesOut(t *testing.T) {
	previousTimeout := automationStopTimeout
	automationStopTimeout = 20 * time.Millisecond
	defer func() {
		automationStopTimeout = previousTimeout
	}()

	app := &App{
		automation: blockingAutomation{done: context.Background()},
	}

	startedAt := time.Now()
	if err := app.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}
	elapsed := time.Since(startedAt)
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("expected close to return promptly after timeout, took %s", elapsed)
	}
}

func TestNewAgentEngineSelectorDefaultsToLegacy(t *testing.T) {
	legacy := appTestAgentEngine{}
	selector, err := newAgentEngineSelector(
		config.Config{Paths: config.PathsConfig{ChatsDir: t.TempDir()}},
		legacy,
		nil,
		appTestToolExecutor{},
		&http.Client{},
	)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}

	selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	if selection.Name != engine.LegacyName || selection.Engine != legacy {
		t.Fatalf("expected legacy engine, got %#v", selection)
	}
}

func TestNewAgentEngineSelectorInitializesZenForgeWhenEnabled(t *testing.T) {
	legacy := appTestAgentEngine{}
	selector, err := newAgentEngineSelector(
		config.Config{
			Paths:    config.PathsConfig{ChatsDir: t.TempDir()},
			ZenForge: config.ZenForgeConfig{Enabled: true},
		},
		legacy,
		&models.ModelRegistry{},
		appTestToolExecutor{},
		&http.Client{},
	)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}

	selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	if selection.Name != engine.ZenForgeName {
		t.Fatalf("expected ZenForge engine, got %#v", selection)
	}
}

func TestZenForgePersistenceDirIsIsolatedUnderChats(t *testing.T) {
	chatsDir := t.TempDir()
	want := chatsDir + string(filepath.Separator) + ".zenforge"
	if got := zenForgePersistenceDir(chatsDir); got != want {
		t.Fatalf("persistence dir = %q, want %q", got, want)
	}
}

func TestNewAgentEngineSelectorHonorsInitializationFallback(t *testing.T) {
	legacy := appTestAgentEngine{}
	selector, err := newAgentEngineSelector(
		config.Config{
			Paths: config.PathsConfig{ChatsDir: t.TempDir()},
			ZenForge: config.ZenForgeConfig{
				Enabled:             true,
				FallbackOnInitError: true,
			},
		},
		legacy,
		nil,
		appTestToolExecutor{},
		&http.Client{},
	)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}

	selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
	if err != nil {
		t.Fatalf("select engine: %v", err)
	}
	if selection.Name != engine.LegacyName || selection.Engine != legacy {
		t.Fatalf("expected legacy fallback, got %#v", selection)
	}

	strictSelector, err := newAgentEngineSelector(
		config.Config{
			Paths:    config.PathsConfig{ChatsDir: t.TempDir()},
			ZenForge: config.ZenForgeConfig{Enabled: true},
		},
		legacy,
		nil,
		appTestToolExecutor{},
		&http.Client{},
	)
	if err != nil {
		t.Fatalf("new strict selector: %v", err)
	}
	if _, err := strictSelector.Select(context.Background(), contracts.EngineSelectionInput{}); !errors.Is(err, engine.ErrZenForgeInitialization) {
		t.Fatalf("expected ZenForge initialization error, got %v", err)
	}
}

type appTestAgentEngine struct{}

func (appTestAgentEngine) Stream(context.Context, api.QueryRequest, contracts.QuerySession) (contracts.AgentStream, error) {
	return nil, nil
}

type appTestToolExecutor struct{}

func (appTestToolExecutor) Definitions() []api.ToolDetailResponse { return nil }

func (appTestToolExecutor) Invoke(context.Context, string, map[string]any, *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	return contracts.ToolExecutionResult{}, nil
}
