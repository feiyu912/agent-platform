package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

const (
	LegacyName   = "legacy"
	ZenForgeName = "zenforge"
)

var ErrZenForgeInitialization = errors.New("zenforge engine initialization failed")

type ZenForgeInitializationError struct {
	Cause error
}

func (e *ZenForgeInitializationError) Error() string {
	return fmt.Sprintf("%v: %v", ErrZenForgeInitialization, e.Cause)
}

func (e *ZenForgeInitializationError) Unwrap() error {
	return e.Cause
}

func (e *ZenForgeInitializationError) Is(target error) bool {
	return target == ErrZenForgeInitialization
}

type Factory interface {
	New(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error)
}

type FactoryFunc func(context.Context, contracts.EngineSelectionInput) (contracts.AgentEngine, error)

func (f FactoryFunc) New(ctx context.Context, input contracts.EngineSelectionInput) (contracts.AgentEngine, error) {
	return f(ctx, input)
}

type Selector struct {
	config  config.ZenForgeConfig
	legacy  contracts.AgentEngine
	factory Factory

	once           sync.Once
	zenForgeEngine contracts.AgentEngine
	zenForgeErr    error
}

func NewSelector(cfg config.ZenForgeConfig, legacy contracts.AgentEngine, factory Factory) (*Selector, error) {
	if isNilAgentEngine(legacy) {
		return nil, errors.New("legacy agent engine is required")
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Selector{
		config:  normalized,
		legacy:  legacy,
		factory: factory,
	}, nil
}

func (s *Selector) Select(ctx context.Context, input contracts.EngineSelectionInput) (contracts.EngineSelection, error) {
	if s.route(input) == LegacyName {
		return contracts.EngineSelection{Name: LegacyName, Engine: s.legacy}, nil
	}

	s.once.Do(func() {
		if s.factory == nil {
			s.zenForgeErr = errors.New("zenforge engine factory is required")
			return
		}
		s.zenForgeEngine, s.zenForgeErr = s.factory.New(ctx, input)
		if s.zenForgeErr == nil && isNilAgentEngine(s.zenForgeEngine) {
			s.zenForgeErr = errors.New("zenforge engine factory returned nil engine")
		}
	})
	if s.zenForgeErr != nil {
		if s.config.FallbackOnInitError {
			return contracts.EngineSelection{Name: LegacyName, Engine: s.legacy}, nil
		}
		return contracts.EngineSelection{}, &ZenForgeInitializationError{Cause: s.zenForgeErr}
	}
	return contracts.EngineSelection{Name: ZenForgeName, Engine: s.zenForgeEngine}, nil
}

func (s *Selector) route(input contracts.EngineSelectionInput) string {
	selected := LegacyName
	if s.config.Enabled {
		selected = ZenForgeName
	}
	if value, ok := s.config.AgentOverrides[strings.TrimSpace(input.Session.AgentKey)]; ok {
		selected = value
	}
	if value, ok := s.config.ChatOverrides[strings.TrimSpace(input.Session.ChatID)]; ok {
		selected = value
	}
	if value, ok := s.config.RunOverrides[strings.TrimSpace(input.Session.RunID)]; ok {
		selected = value
	}
	return selected
}

func normalizeConfig(source config.ZenForgeConfig) (config.ZenForgeConfig, error) {
	var err error
	if source.AgentOverrides, err = normalizeOverrides("agent", source.AgentOverrides); err != nil {
		return config.ZenForgeConfig{}, err
	}
	if source.ChatOverrides, err = normalizeOverrides("chat", source.ChatOverrides); err != nil {
		return config.ZenForgeConfig{}, err
	}
	if source.RunOverrides, err = normalizeOverrides("run", source.RunOverrides); err != nil {
		return config.ZenForgeConfig{}, err
	}
	return source, nil
}

func normalizeOverrides(scope string, source map[string]string) (map[string]string, error) {
	normalized := make(map[string]string, len(source))
	for rawKey, rawValue := range source {
		key := strings.TrimSpace(rawKey)
		value := strings.ToLower(strings.TrimSpace(rawValue))
		if key == "" {
			return nil, fmt.Errorf("zenforge %s override contains an empty key", scope)
		}
		if value != LegacyName && value != ZenForgeName {
			return nil, fmt.Errorf("zenforge %s override %q must select legacy or zenforge", scope, key)
		}
		if _, exists := normalized[key]; exists {
			return nil, fmt.Errorf("zenforge %s override contains duplicate key %q", scope, key)
		}
		normalized[key] = value
	}
	return normalized, nil
}

func isNilAgentEngine(engine contracts.AgentEngine) bool {
	if engine == nil {
		return true
	}
	value := reflect.ValueOf(engine)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
