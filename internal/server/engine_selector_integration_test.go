package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type recordingEngineSelector struct {
	mu        sync.Mutex
	selection contracts.EngineSelection
	err       error
	calls     int
	inputs    []contracts.EngineSelectionInput
}

func (s *recordingEngineSelector) Select(_ context.Context, input contracts.EngineSelectionInput) (contracts.EngineSelection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.inputs = append(s.inputs, input)
	return s.selection, s.err
}

func (s *recordingEngineSelector) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type recordingAgentEngine struct {
	mu       sync.Mutex
	deltas   []contracts.AgentDelta
	err      error
	calls    int
	requests []api.QueryRequest
}

func (e *recordingAgentEngine) Stream(_ context.Context, req api.QueryRequest, _ contracts.QuerySession) (contracts.AgentStream, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.requests = append(e.requests, req)
	if e.err != nil {
		return nil, e.err
	}
	return &selectorAgentStream{deltas: append([]contracts.AgentDelta(nil), e.deltas...)}, nil
}

func (e *recordingAgentEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type selectorAgentStream struct {
	deltas []contracts.AgentDelta
	index  int
}

func (s *selectorAgentStream) Next() (contracts.AgentDelta, error) {
	if s.index >= len(s.deltas) {
		return nil, io.EOF
	}
	delta := s.deltas[s.index]
	s.index++
	return delta, nil
}

func (*selectorAgentStream) Close() error { return nil }

type approvalRecordingEngine struct {
	recordingAgentEngine
	ready chan struct{}
}

func (e *approvalRecordingEngine) Stream(ctx context.Context, req api.QueryRequest, _ contracts.QuerySession) (contracts.AgentStream, error) {
	e.mu.Lock()
	e.calls++
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	control := contracts.RunControlFromContext(ctx)
	if control == nil {
		return nil, errors.New("run control missing")
	}
	control.ExpectSubmit(contracts.AwaitingSubmitContext{
		AwaitingID: "approval-fixed-engine",
		Mode:       "approval",
		ItemCount:  1,
		NoTimeout:  true,
	})
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	return &approvalSelectorStream{ctx: ctx, control: control, ready: e.ready, runID: req.RunID, chatID: req.ChatID}, nil
}

type approvalSelectorStream struct {
	ctx     context.Context
	control *contracts.RunControl
	ready   chan struct{}
	runID   string
	chatID  string
	state   int
	submit  contracts.SubmitResult
}

func (s *approvalSelectorStream) Next() (contracts.AgentDelta, error) {
	switch s.state {
	case 0:
		s.state++
		close(s.ready)
		return contracts.DeltaAwaitAsk{
			AwaitingID: "approval-fixed-engine",
			Mode:       "approval",
			RunID:      s.runID,
			Approvals:  []any{map[string]any{"id": "tool-fixed", "title": "Approve fixed engine"}},
		}, nil
	case 1:
		result, err := s.control.AwaitSubmit(s.ctx, "approval-fixed-engine")
		if err != nil {
			return nil, err
		}
		s.submit = result
		s.state++
		return contracts.DeltaRequestSubmit{
			ChatID:     s.chatID,
			RunID:      s.runID,
			AwaitingID: "approval-fixed-engine",
			SubmitID:   result.Request.SubmitID,
			Params:     result.Request.Params,
		}, nil
	case 2:
		s.state++
		return contracts.DeltaAwaitingAnswer{
			AwaitingID: "approval-fixed-engine",
			Answer:     map[string]any{"status": s.submit.Status, "mode": "approval"},
		}, nil
	case 3:
		s.state++
		return contracts.DeltaContent{Text: "selected-after-approval"}, nil
	default:
		return nil, io.EOF
	}
}

func (*approvalSelectorStream) Close() error { return nil }

func installRecordingSelector(fixture testFixture, selected, legacy *recordingAgentEngine, selectorErr error) *recordingEngineSelector {
	selector := &recordingEngineSelector{
		selection: contracts.EngineSelection{Name: "zenforge", Engine: selected},
		err:       selectorErr,
	}
	fixture.server.deps.Agent = legacy
	fixture.server.deps.EngineSelector = selector
	return selector
}

func TestHTTPQueryUsesSelectedEngineExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		sync bool
	}{
		{name: "sync", sync: true},
		{name: "async"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestFixture(t)
			selected := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "selected-engine"}}}
			legacy := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "legacy-engine"}}}
			selector := installRecordingSelector(fixture, selected, legacy, nil)

			req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"engine selection","agentKey":"mock-agent"}`))
			if tc.sync {
				req = req.WithContext(withSyncQueryContext(req.Context()))
			}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "selected-engine") || strings.Contains(rec.Body.String(), "legacy-engine") {
				t.Fatalf("query did not use selected engine: %s", rec.Body.String())
			}
			assertEngineSelectionCalls(t, selector, selected, legacy, 1)
		})
	}
}

func TestEngineSelectionErrorPrecedesAnyStreamOutput(t *testing.T) {
	fixture := newTestFixture(t)
	selected := &recordingAgentEngine{}
	legacy := &recordingAgentEngine{}
	selectorErr := errors.New("zenforge initialization failed")
	selector := installRecordingSelector(fixture, selected, legacy, selectorErr)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"must fail before stream","agentKey":"mock-agent"}`))
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), selectorErr.Error()) {
		t.Fatalf("response does not expose selector error: %s", rec.Body.String())
	}
	if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") || strings.Contains(rec.Body.String(), `"type":"request.query"`) {
		t.Fatalf("selector error leaked stream output: headers=%v body=%s", rec.Header(), rec.Body.String())
	}
	assertEngineSelectionCalls(t, selector, selected, legacy, 0)
}

func TestSelectedEngineStreamErrorDoesNotFallbackToLegacy(t *testing.T) {
	fixture := newTestFixture(t)
	streamErr := errors.New("zenforge stream failed")
	selected := &recordingAgentEngine{err: streamErr}
	legacy := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "legacy-fallback"}}}
	selector := installRecordingSelector(fixture, selected, legacy, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"do not fallback","agentKey":"mock-agent"}`))
	req = req.WithContext(withSyncQueryContext(req.Context()))
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), streamErr.Error()) {
		t.Fatalf("missing selected engine stream error: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "legacy-fallback") {
		t.Fatalf("response contains legacy fallback output: %s", rec.Body.String())
	}
	assertEngineSelectionCalls(t, selector, selected, legacy, 1)
}

func TestLegacyEngineRemainsCompatibleWithoutSelector(t *testing.T) {
	fixture := newTestFixture(t)
	legacy := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "legacy-compatible"}}}
	fixture.server.deps.Agent = legacy
	fixture.server.deps.EngineSelector = nil

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"legacy request","agentKey":"mock-agent"}`))
	req = req.WithContext(withSyncQueryContext(req.Context()))
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "legacy-compatible") {
		t.Fatalf("legacy query failed status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := legacy.callCount(); got != 1 {
		t.Fatalf("legacy engine calls = %d, want 1", got)
	}
}

func TestApprovalSubmitContinuesOnFixedSelectedEngine(t *testing.T) {
	fixture := newTestFixture(t)
	selected := &approvalRecordingEngine{ready: make(chan struct{})}
	legacy := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "legacy-after-submit"}}}
	selector := &recordingEngineSelector{selection: contracts.EngineSelection{Name: "zenforge", Engine: selected}}
	fixture.server.deps.Agent = legacy
	fixture.server.deps.EngineSelector = selector

	const runID = "run-fixed-engine-approval"
	const chatID = "chat-fixed-engine-approval"
	queryReq := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"approval continuity","agentKey":"mock-agent","runId":"`+runID+`","chatId":"`+chatID+`"}`))
	queryRec := httptest.NewRecorder()
	queryDone := make(chan struct{})
	go func() {
		fixture.server.ServeHTTP(queryRec, queryReq)
		close(queryDone)
	}()

	select {
	case <-selected.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("selected engine did not reach approval")
	}
	attachRec := httptest.NewRecorder()
	attachReq := httptest.NewRequest(http.MethodGet, "/api/attach?runId="+runID+"&agentKey=mock-agent&lastSeq=0", nil)
	attachDone := make(chan struct{})
	go func() {
		fixture.server.ServeHTTP(attachRec, attachReq)
		close(attachDone)
	}()

	submitRec := httptest.NewRecorder()
	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"requestId":"submit-fixed","agentKey":"mock-agent","chatId":"`+chatID+`","runId":"`+runID+`","awaitingId":"approval-fixed-engine","params":[{"id":"tool-fixed","decision":"approve"}]}`))
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK || !strings.Contains(submitRec.Body.String(), `"accepted":true`) {
		t.Fatalf("submit failed status=%d body=%s", submitRec.Code, submitRec.Body.String())
	}

	select {
	case <-queryDone:
	case <-time.After(2 * time.Second):
		t.Fatal("query did not continue after approval")
	}
	select {
	case <-attachDone:
	case <-time.After(2 * time.Second):
		t.Fatal("attached observer did not finish with selected-engine run")
	}
	streamBody := queryRec.Body.String()
	for _, want := range []string{`"type":"awaiting.ask"`, `"type":"request.submit"`, "selected-after-approval"} {
		if !strings.Contains(streamBody, want) {
			t.Fatalf("SSE mapper output missing %q: %s", want, streamBody)
		}
	}
	if strings.Contains(streamBody, "legacy-after-submit") {
		t.Fatalf("approval continued through legacy engine: %s", streamBody)
	}
	if attachRec.Code != http.StatusOK || !strings.Contains(attachRec.Body.String(), "selected-after-approval") {
		t.Fatalf("attach did not continue selected-engine stream status=%d body=%s", attachRec.Code, attachRec.Body.String())
	}
	if selector.callCount() != 1 || selected.callCount() != 1 || legacy.callCount() != 0 {
		t.Fatalf("unexpected calls selector=%d selected=%d legacy=%d", selector.callCount(), selected.callCount(), legacy.callCount())
	}
}

func assertEngineSelectionCalls(t *testing.T, selector *recordingEngineSelector, selected, legacy *recordingAgentEngine, selectedCalls int) {
	t.Helper()
	if selector.callCount() != 1 || selected.callCount() != selectedCalls || legacy.callCount() != 0 {
		t.Fatalf("unexpected calls selector=%d selected=%d legacy=%d", selector.callCount(), selected.callCount(), legacy.callCount())
	}
}

func TestWebSocketQueryUsesSingleSelectedEngine(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 16
			cfg.WebSocket.PingInterval = 30000
		},
	})
	selected := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "ws-selected-engine"}}}
	legacy := &recordingAgentEngine{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "ws-legacy-engine"}}}
	selector := installRecordingSelector(fixture, selected, legacy, nil)

	httpServer := httptest.NewServer(fixture.server)
	defer httpServer.Close()
	conn, _, err := gws.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	readConnectedPush(t, conn)

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/query",
		ID:    "selector-ws-query",
		Payload: ws.MarshalPayload(map[string]any{
			"message":  "websocket selection",
			"agentKey": "mock-agent",
		}),
	}); err != nil {
		t.Fatalf("write websocket query: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var frames strings.Builder
	for !strings.Contains(frames.String(), "ws-selected-engine") {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Fatalf("read websocket stream: %v; frames=%s", readErr, frames.String())
		}
		frames.Write(raw)
	}
	if strings.Contains(frames.String(), "ws-legacy-engine") {
		t.Fatalf("websocket used legacy engine: %s", frames.String())
	}
	assertEngineSelectionCalls(t, selector, selected, legacy, 1)
}

var _ contracts.AgentEngineSelector = (*recordingEngineSelector)(nil)
var _ contracts.AgentEngine = (*recordingAgentEngine)(nil)
var _ contracts.AgentStream = (*selectorAgentStream)(nil)
