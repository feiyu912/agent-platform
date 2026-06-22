package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/anthropic"
	"github.com/feiyu912/zenforge/model/openai"
)

type zenForgeUsageTracker struct {
	mu    sync.Mutex
	last  model.Usage
	total model.Usage
	calls int
}

func (t *zenForgeUsageTracker) add(value model.Usage) {
	t.mu.Lock()
	t.last = value
	t.total.PromptTokens += value.PromptTokens
	t.total.CompletionTokens += value.CompletionTokens
	t.total.TotalTokens += value.TotalTokens
	t.calls++
	t.mu.Unlock()
}

func (t *zenForgeUsageTracker) snapshot() (model.Usage, model.Usage, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last, t.total, t.calls
}

type zenForgeTrackingModel struct {
	next  model.Model
	usage *zenForgeUsageTracker
}

func (m zenForgeTrackingModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	response, err := m.next.Generate(ctx, req)
	if err == nil && response != nil {
		m.usage.add(response.Usage)
	}
	return response, err
}

func (m zenForgeTrackingModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	source, err := m.next.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan model.Event, 32)
	go func() {
		defer close(out)
		for event := range source {
			if event.Type == model.EventUsage {
				m.usage.add(event.Usage)
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (e *ZenForgeAgentEngine) resolveZenForgeModel(_ context.Context, key string) (model.Model, error) {
	definition, provider, err := e.models.Get(key)
	if err != nil {
		return nil, err
	}
	protocol := strings.ToUpper(strings.TrimSpace(definition.Protocol))
	if protocol == "" {
		protocol = "OPENAI"
	}
	protocolDef := provider.Protocol(protocol)
	headers := mergeZenForgeHeaders(protocolDef.Headers, definition.Headers)
	client := cloneZenForgeHTTPClient(e.httpClient, headers)
	modelID := strings.TrimSpace(definition.ModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(provider.DefaultModel)
	}
	if modelID == "" {
		return nil, fmt.Errorf("zenforge model %q has no provider model id", key)
	}
	if strings.TrimSpace(provider.APIKey) == "" {
		return nil, fmt.Errorf("zenforge provider %q has empty API key", provider.Key)
	}
	baseURL, err := zenForgeProviderBaseURL(provider.BaseURL, protocolDef.EndpointPath, protocol)
	if err != nil {
		return nil, fmt.Errorf("zenforge model %q: %w", key, err)
	}
	switch protocol {
	case "OPENAI":
		return openai.New(openai.Config{BaseURL: baseURL, APIKey: provider.APIKey, Model: modelID, HTTPClient: client}), nil
	case "ANTHROPIC":
		return anthropic.New(anthropic.Config{
			BaseURL: baseURL, APIKey: provider.APIKey, Model: modelID,
			AnthropicVersion: headerValue(headers, "Anthropic-Version"), HTTPClient: client,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported zenforge provider protocol %q for model %q", protocol, key)
	}
}

func zenForgeProviderBaseURL(base, endpoint, protocol string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid provider base URL")
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	suffix := "/chat/completions"
	if protocol == "ANTHROPIC" {
		suffix = "/messages"
	}
	endpoint = strings.TrimSuffix(endpoint, suffix)
	if endpoint == "" || endpoint == "/" {
		return base, nil
	}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return strings.TrimRight(endpoint, "/"), nil
	}
	return base + "/" + strings.TrimLeft(endpoint, "/"), nil
}

type zenForgeHeaderTransport struct {
	next    http.RoundTripper
	headers map[string]string
}

func (t zenForgeHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.next.RoundTrip(clone)
}

func cloneZenForgeHTTPClient(source *http.Client, headers map[string]string) *http.Client {
	clone := *source
	next := clone.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	clone.Transport = zenForgeHeaderTransport{next: next, headers: headers}
	return &clone
}

func mergeZenForgeHeaders(values ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, value := range values {
		for key, item := range value {
			out[key] = item
		}
	}
	return out
}

func headerValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
