package engine

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type fakeAgentEngine struct{ id string }

func (*fakeAgentEngine) Stream(context.Context, api.QueryRequest, contracts.QuerySession) (contracts.AgentStream, error) {
	return fakeAgentStream{}, nil
}

type fakeAgentStream struct{}

func (fakeAgentStream) Next() (contracts.AgentDelta, error) { return nil, io.EOF }
func (fakeAgentStream) Close() error                        { return nil }

func TestSelectorDefaultsToLegacyWithoutInitializingZenForge(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	var calls atomic.Int32
	selector := mustSelector(t, config.ZenForgeConfig{FallbackOnInitError: true}, legacy, FactoryFunc(func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
		calls.Add(1)
		return &fakeAgentEngine{id: ZenForgeName}, nil
	}))
	selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
	if err != nil || selection.Name != LegacyName || selection.Engine != legacy {
		t.Fatalf("unexpected selection: %#v, %v", selection, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("factory called %d times", calls.Load())
	}
}

func TestSelectorOverridePrecedence(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	zenforge := &fakeAgentEngine{id: ZenForgeName}
	cfg := config.ZenForgeConfig{
		AgentOverrides: map[string]string{"agent": ZenForgeName},
		ChatOverrides:  map[string]string{"chat": LegacyName},
		RunOverrides:   map[string]string{"run": ZenForgeName},
	}
	selector := mustSelector(t, cfg, legacy, FactoryFunc(func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
		return zenforge, nil
	}))
	input := contracts.EngineSelectionInput{Session: contracts.QuerySession{AgentKey: "agent", ChatID: "chat", RunID: "run"}}
	selection, err := selector.Select(context.Background(), input)
	if err != nil || selection.Name != ZenForgeName || selection.Engine != zenforge {
		t.Fatalf("run override did not win: %#v, %v", selection, err)
	}

	selector = mustSelector(t, config.ZenForgeConfig{Enabled: true, ChatOverrides: map[string]string{"chat": LegacyName}}, legacy, nil)
	selection, err = selector.Select(context.Background(), contracts.EngineSelectionInput{Session: contracts.QuerySession{ChatID: "chat"}})
	if err != nil || selection.Name != LegacyName {
		t.Fatalf("chat override did not beat global: %#v, %v", selection, err)
	}
}

func TestSelectorPassesIdentityInputToLazyFactory(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	want := contracts.EngineSelectionInput{
		Request: api.QueryRequest{RequestID: "request", RunID: "request-run", ChatID: "request-chat", AgentKey: "request-agent"},
		Session: contracts.QuerySession{RunID: "run", ChatID: "chat", AgentKey: "agent"},
	}
	var got contracts.EngineSelectionInput
	selector := mustSelector(t, config.ZenForgeConfig{Enabled: true}, legacy, FactoryFunc(func(_ context.Context, input contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
		got = input
		return &fakeAgentEngine{id: ZenForgeName}, nil
	}))
	if _, err := selector.Select(context.Background(), want); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got.Request.RequestID != want.Request.RequestID || got.Session.RunID != want.Session.RunID || got.Session.ChatID != want.Session.ChatID || got.Session.AgentKey != want.Session.AgentKey {
		t.Fatalf("factory input mismatch: %#v", got)
	}
}

func TestSelectorInitializationFallback(t *testing.T) {
	initErr := errors.New("boom")
	legacy := &fakeAgentEngine{id: LegacyName}
	for _, fallback := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[fallback], func(t *testing.T) {
			selector := mustSelector(t, config.ZenForgeConfig{Enabled: true, FallbackOnInitError: fallback}, legacy, FactoryFunc(func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
				return nil, initErr
			}))
			selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
			if fallback {
				if err != nil || selection.Name != LegacyName || selection.Engine != legacy {
					t.Fatalf("unexpected fallback: %#v, %v", selection, err)
				}
				return
			}
			if !errors.Is(err, ErrZenForgeInitialization) || !errors.Is(err, initErr) {
				t.Fatalf("expected typed initialization error, got %v", err)
			}
		})
	}
}

func TestSelectorConcurrentInitializationRunsOnce(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	zenforge := &fakeAgentEngine{id: ZenForgeName}
	var calls atomic.Int32
	selector := mustSelector(t, config.ZenForgeConfig{Enabled: true}, legacy, FactoryFunc(func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
		calls.Add(1)
		return zenforge, nil
	}))

	const count = 64
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
			if err != nil {
				errs <- err
				return
			}
			if selection.Name != ZenForgeName || selection.Engine != zenforge {
				errs <- errors.New("unexpected engine selection")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times", calls.Load())
	}
}

func TestSelectorConcurrentInitializationFailureIsCached(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	initErr := errors.New("init failed")
	var calls atomic.Int32
	selector := mustSelector(t, config.ZenForgeConfig{Enabled: true}, legacy, FactoryFunc(func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
		calls.Add(1)
		return nil, initErr
	}))

	const count = 32
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := selector.Select(context.Background(), contracts.EngineSelectionInput{})
			if !errors.Is(err, ErrZenForgeInitialization) || !errors.Is(err, initErr) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("factory called %d times", calls.Load())
	}
}

func TestSelectorOwnsOverrideMaps(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	overrides := map[string]string{"agent": ZenForgeName}
	selector := mustSelector(t, config.ZenForgeConfig{AgentOverrides: overrides}, legacy, FactoryFunc(func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
		return &fakeAgentEngine{id: ZenForgeName}, nil
	}))
	overrides["agent"] = LegacyName
	selection, err := selector.Select(context.Background(), contracts.EngineSelectionInput{Session: contracts.QuerySession{AgentKey: "agent"}})
	if err != nil || selection.Name != ZenForgeName {
		t.Fatalf("selector retained caller-owned config: %#v, %v", selection, err)
	}
}

func TestSelectorRequiresLegacy(t *testing.T) {
	if _, err := NewSelector(config.ZenForgeConfig{}, nil, nil); err == nil {
		t.Fatal("expected missing legacy engine error")
	}
	var typedNil *fakeAgentEngine
	if _, err := NewSelector(config.ZenForgeConfig{}, typedNil, nil); err == nil {
		t.Fatal("expected typed nil legacy engine error")
	}
}

func TestSelectorRejectsInvalidOverrides(t *testing.T) {
	legacy := &fakeAgentEngine{id: LegacyName}
	if _, err := NewSelector(config.ZenForgeConfig{RunOverrides: map[string]string{"run": "other"}}, legacy, nil); err == nil {
		t.Fatal("expected invalid override error")
	}
}

func mustSelector(t *testing.T, cfg config.ZenForgeConfig, legacy contracts.AgentEngine, factory Factory) *Selector {
	t.Helper()
	selector, err := NewSelector(cfg, legacy, factory)
	if err != nil {
		t.Fatalf("new selector: %v", err)
	}
	return selector
}
