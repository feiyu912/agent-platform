package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

const HookName = "lsp_diagnostics"

type Manager struct {
	cfg       config.LSPDiagnosticsHookConfig
	mu        sync.Mutex
	sessions  map[string]*serverSession
	lookPath  func(string) (string, error)
	startProc func(context.Context, string, []string, string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error)
}

func NewManager(cfg config.LSPDiagnosticsHookConfig) *Manager {
	cfg = normalizeConfig(cfg)
	return &Manager{
		cfg:      cfg,
		sessions: map[string]*serverSession{},
		lookPath: exec.LookPath,
		startProc: func(_ context.Context, command string, args []string, workspaceRoot string) (*exec.Cmd, io.WriteCloser, io.ReadCloser, error) {
			cmd := exec.Command(command, args...)
			cmd.Dir = workspaceRoot
			stdin, err := cmd.StdinPipe()
			if err != nil {
				return nil, nil, nil, err
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return nil, nil, nil, err
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				return nil, nil, nil, err
			}
			return cmd, stdin, stdout, nil
		},
	}
}

func (m *Manager) AfterFileChange(ctx context.Context, event contracts.FileChangeEvent) contracts.FileChangeHookResult {
	result := contracts.FileChangeHookResult{
		Name:     HookName,
		FilePath: event.FilePath,
	}
	if m == nil {
		result.Status = "skipped"
		result.Reason = "manager_not_configured"
		return result
	}
	if !m.cfg.Enabled {
		result.Status = "skipped"
		result.Reason = "disabled"
		return result
	}
	workspaceRoot := strings.TrimSpace(event.WorkspaceRoot)
	if workspaceRoot == "" {
		result.Status = "skipped"
		result.Reason = "missing_workspace_root"
		return result
	}
	languageID := strings.ToLower(strings.TrimSpace(event.LanguageID))
	if languageID == "" {
		languageID = DetectLanguageID(event.FilePath)
	}
	result.LanguageID = languageID
	if languageID == "" || !m.languageAllowed(languageID) {
		result.Status = "skipped"
		result.Reason = "unsupported_language"
		return result
	}
	server := m.cfg.Servers[languageID]
	if strings.TrimSpace(server.Command) == "" {
		result.Status = "skipped"
		result.Reason = "server_not_configured"
		return result
	}
	if _, err := m.lookPath(server.Command); err != nil {
		result.Status = "skipped"
		result.Reason = "server_not_found"
		result.Message = err.Error()
		return result
	}

	timeout := time.Duration(m.cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	session, err := m.session(callCtx, workspaceRoot, languageID, server)
	if err != nil {
		return hookErrorResult(result, callCtx, err)
	}
	diagnostics, err := session.diagnose(callCtx, event.FilePath, languageID, event.Content)
	if err != nil {
		return hookErrorResult(result, callCtx, err)
	}
	result.Status = "ok"
	result.Diagnostics = diagnostics
	return result
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	sessions := make([]*serverSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = map[string]*serverSession{}
	m.mu.Unlock()

	var errs []string
	for _, session := range sessions {
		if err := session.close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) session(ctx context.Context, workspaceRoot string, languageID string, server config.LSPServerConfig) (*serverSession, error) {
	key := filepath.Clean(workspaceRoot) + "\x00" + languageID
	m.mu.Lock()
	if session := m.sessions[key]; session != nil && !session.closed() {
		m.mu.Unlock()
		return session, nil
	}
	m.mu.Unlock()

	cmd, stdin, stdout, err := m.startProc(ctx, server.Command, append([]string(nil), server.Args...), workspaceRoot)
	if err != nil {
		return nil, err
	}
	session := newServerSession(cmd, stdin, stdout, workspaceRoot)
	if err := session.initialize(ctx); err != nil {
		_ = session.close()
		return nil, err
	}

	m.mu.Lock()
	if existing := m.sessions[key]; existing != nil && !existing.closed() {
		m.mu.Unlock()
		_ = session.close()
		return existing, nil
	}
	m.sessions[key] = session
	m.mu.Unlock()
	return session, nil
}

func (m *Manager) languageAllowed(languageID string) bool {
	for _, allowed := range m.cfg.Languages {
		if strings.EqualFold(strings.TrimSpace(allowed), languageID) {
			return true
		}
	}
	return false
}

func hookErrorResult(result contracts.FileChangeHookResult, ctx context.Context, err error) contracts.FileChangeHookResult {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		result.Status = "timeout"
		result.Reason = "timeout"
	} else {
		result.Status = "failed"
		result.Reason = "lsp_error"
	}
	result.Message = err.Error()
	return result
}

func DetectLanguageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py", ".pyw":
		return "python"
	case ".rs":
		return "rust"
	default:
		return ""
	}
}

func normalizeConfig(cfg config.LSPDiagnosticsHookConfig) config.LSPDiagnosticsHookConfig {
	if cfg.TimeoutMs <= 0 {
		cfg.TimeoutMs = 3000
	}
	if len(cfg.Languages) == 0 {
		cfg.Languages = []string{"go", "typescript", "javascript", "python", "rust"}
	}
	if len(cfg.Servers) == 0 {
		cfg.Servers = map[string]config.LSPServerConfig{
			"go":         {Command: "gopls"},
			"typescript": {Command: "typescript-language-server", Args: []string{"--stdio"}},
			"javascript": {Command: "typescript-language-server", Args: []string{"--stdio"}},
			"python":     {Command: "pyright-langserver", Args: []string{"--stdio"}},
			"rust":       {Command: "rust-analyzer"},
		}
	}
	cfg.Languages = normalizeLanguageIDs(cfg.Languages)
	servers := make(map[string]config.LSPServerConfig, len(cfg.Servers))
	for key, server := range cfg.Servers {
		languageID := strings.ToLower(strings.TrimSpace(key))
		if languageID == "" {
			continue
		}
		server.Command = strings.TrimSpace(server.Command)
		server.Args = append([]string(nil), server.Args...)
		servers[languageID] = server
	}
	cfg.Servers = servers
	return cfg
}

func normalizeLanguageIDs(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		languageID := strings.ToLower(strings.TrimSpace(value))
		if languageID == "" {
			continue
		}
		if _, ok := seen[languageID]; ok {
			continue
		}
		seen[languageID] = struct{}{}
		out = append(out, languageID)
	}
	return out
}

type serverSession struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	workspaceRoot string

	nextID   atomic.Int64
	done     chan struct{}
	closeMu  sync.Mutex
	isClosed bool

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponse

	diagnosticsMu sync.Mutex
	diagnostics   map[string][]contracts.LSPDiagnostic
	waiters       map[string][]chan struct{}

	openedMu sync.Mutex
	opened   map[string]int
}

func newServerSession(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser, workspaceRoot string) *serverSession {
	session := &serverSession{
		cmd:           cmd,
		stdin:         stdin,
		stdout:        stdout,
		workspaceRoot: workspaceRoot,
		done:          make(chan struct{}),
		pending:       map[int64]chan rpcResponse{},
		diagnostics:   map[string][]contracts.LSPDiagnostic{},
		waiters:       map[string][]chan struct{}{},
		opened:        map[string]int{},
	}
	go session.readLoop()
	return session
}

func (s *serverSession) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": nil,
		"rootUri":   pathToFileURI(s.workspaceRoot),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{
					"didSave": true,
				},
				"publishDiagnostics": map[string]any{
					"relatedInformation": false,
				},
			},
			"workspace": map[string]any{
				"configuration": false,
			},
		},
	}
	if _, err := s.request(ctx, "initialize", params); err != nil {
		return err
	}
	return s.notify("initialized", map[string]any{})
}

func (s *serverSession) diagnose(ctx context.Context, filePath string, languageID string, content []byte) ([]contracts.LSPDiagnostic, error) {
	if len(content) == 0 {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, err
		}
		content = data
	}
	uri := pathToFileURI(filePath)
	s.clearDiagnostics(uri)
	version, opened := s.nextVersion(uri)
	textDocument := map[string]any{
		"uri":        uri,
		"languageId": languageID,
		"version":    version,
		"text":       string(content),
	}
	if !opened {
		if err := s.notify("textDocument/didOpen", map[string]any{"textDocument": textDocument}); err != nil {
			return nil, err
		}
	} else {
		if err := s.notify("textDocument/didChange", map[string]any{
			"textDocument": map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{
				{"text": string(content)},
			},
		}); err != nil {
			return nil, err
		}
	}
	if err := s.notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"text":         string(content),
	}); err != nil {
		return nil, err
	}
	return s.waitDiagnostics(ctx, uri)
}

func (s *serverSession) nextVersion(uri string) (int, bool) {
	s.openedMu.Lock()
	defer s.openedMu.Unlock()
	current := s.opened[uri]
	s.opened[uri] = current + 1
	return s.opened[uri], current > 0
}

func (s *serverSession) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	s.pendingMu.Lock()
	s.pending[id] = ch
	s.pendingMu.Unlock()

	if err := s.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		s.removePending(id)
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.removePending(id)
		return nil, ctx.Err()
	case <-s.done:
		s.removePending(id)
		return nil, errors.New("language server exited")
	case response := <-ch:
		if response.Error != nil {
			return nil, fmt.Errorf("%s: %s", response.Error.CodeString(), response.Error.Message)
		}
		return response.Result, nil
	}
}

func (s *serverSession) notify(method string, params any) error {
	return s.send(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (s *serverSession) send(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var message bytes.Buffer
	fmt.Fprintf(&message, "Content-Length: %d\r\n\r\n", len(data))
	message.Write(data)

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(message.Bytes())
	return err
}

func (s *serverSession) readLoop() {
	defer close(s.done)
	reader := bufio.NewReader(s.stdout)
	for {
		data, err := readLSPMessage(reader)
		if err != nil {
			s.failPending(err)
			return
		}
		var envelope rpcEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil {
			continue
		}
		if envelope.Method == "textDocument/publishDiagnostics" {
			s.handlePublishDiagnostics(envelope.Params)
			continue
		}
		if envelope.ID != nil && envelope.Method == "" {
			s.handleResponse(envelope)
			continue
		}
		if envelope.ID != nil && envelope.Method != "" {
			_ = s.send(map[string]any{
				"jsonrpc": "2.0",
				"id":      envelope.ID,
				"result":  nil,
			})
		}
	}
}

func readLSPMessage(reader *bufio.Reader) ([]byte, error) {
	contentLength := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, err
			}
			contentLength = parsed
		}
	}
	if contentLength < 0 {
		return nil, errors.New("missing Content-Length")
	}
	data := make([]byte, contentLength)
	_, err := io.ReadFull(reader, data)
	return data, err
}

func (s *serverSession) handleResponse(envelope rpcEnvelope) {
	id, ok := rpcIDToInt(envelope.ID)
	if !ok {
		return
	}
	s.pendingMu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.pendingMu.Unlock()
	if ch != nil {
		ch <- rpcResponse{Result: envelope.Result, Error: envelope.Error}
	}
}

func (s *serverSession) handlePublishDiagnostics(raw json.RawMessage) {
	var params publishDiagnosticsParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return
	}
	uri := strings.TrimSpace(params.URI)
	if uri == "" {
		return
	}
	diagnostics := make([]contracts.LSPDiagnostic, 0, len(params.Diagnostics))
	for _, item := range params.Diagnostics {
		diagnostics = append(diagnostics, contracts.LSPDiagnostic{
			Severity: severityLabel(item.Severity),
			Message:  item.Message,
			Source:   item.Source,
			Code:     diagnosticCodeString(item.Code),
			Range: contracts.LSPRange{
				Start: contracts.LSPPosition{Line: item.Range.Start.Line, Character: item.Range.Start.Character},
				End:   contracts.LSPPosition{Line: item.Range.End.Line, Character: item.Range.End.Character},
			},
		})
	}

	s.diagnosticsMu.Lock()
	s.diagnostics[uri] = diagnostics
	waiters := s.waiters[uri]
	delete(s.waiters, uri)
	s.diagnosticsMu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
}

func (s *serverSession) clearDiagnostics(uri string) {
	s.diagnosticsMu.Lock()
	delete(s.diagnostics, uri)
	s.diagnosticsMu.Unlock()
}

func (s *serverSession) waitDiagnostics(ctx context.Context, uri string) ([]contracts.LSPDiagnostic, error) {
	s.diagnosticsMu.Lock()
	if diagnostics, ok := s.diagnostics[uri]; ok {
		s.diagnosticsMu.Unlock()
		return diagnostics, nil
	}
	waiter := make(chan struct{})
	s.waiters[uri] = append(s.waiters[uri], waiter)
	s.diagnosticsMu.Unlock()

	select {
	case <-ctx.Done():
		s.removeDiagnosticsWaiter(uri, waiter)
		return nil, ctx.Err()
	case <-s.done:
		s.removeDiagnosticsWaiter(uri, waiter)
		return nil, errors.New("language server exited")
	case <-waiter:
		s.diagnosticsMu.Lock()
		diagnostics := append([]contracts.LSPDiagnostic(nil), s.diagnostics[uri]...)
		s.diagnosticsMu.Unlock()
		return diagnostics, nil
	}
}

func (s *serverSession) removeDiagnosticsWaiter(uri string, target chan struct{}) {
	s.diagnosticsMu.Lock()
	defer s.diagnosticsMu.Unlock()
	waiters := s.waiters[uri]
	for idx, waiter := range waiters {
		if waiter != target {
			continue
		}
		waiters = append(waiters[:idx], waiters[idx+1:]...)
		break
	}
	if len(waiters) == 0 {
		delete(s.waiters, uri)
		return
	}
	s.waiters[uri] = waiters
}

func (s *serverSession) removePending(id int64) {
	s.pendingMu.Lock()
	delete(s.pending, id)
	s.pendingMu.Unlock()
}

func (s *serverSession) failPending(err error) {
	s.pendingMu.Lock()
	pending := s.pending
	s.pending = map[int64]chan rpcResponse{}
	s.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- rpcResponse{Error: &rpcError{Code: -32000, Message: err.Error()}}
	}
}

func (s *serverSession) closed() bool {
	s.closeMu.Lock()
	isClosed := s.isClosed
	s.closeMu.Unlock()
	if isClosed {
		return true
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *serverSession) close() error {
	s.closeMu.Lock()
	if s.isClosed {
		s.closeMu.Unlock()
		return nil
	}
	s.isClosed = true
	s.closeMu.Unlock()

	_ = s.notify("shutdown", nil)
	_ = s.notify("exit", nil)
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		_ = s.cmd.Process.Kill()
		return <-done
	}
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *rpcError
}

type rpcError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func (e rpcError) CodeString() string {
	if e.Code == 0 {
		return "lsp_error"
	}
	return strconv.Itoa(e.Code)
}

type publishDiagnosticsParams struct {
	URI         string               `json:"uri"`
	Diagnostics []diagnosticWireItem `json:"diagnostics"`
}

type diagnosticWireItem struct {
	Range    diagnosticWireRange `json:"range"`
	Severity int                 `json:"severity,omitempty"`
	Code     any                 `json:"code,omitempty"`
	Source   string              `json:"source,omitempty"`
	Message  string              `json:"message"`
}

type diagnosticWireRange struct {
	Start diagnosticWirePosition `json:"start"`
	End   diagnosticWirePosition `json:"end"`
}

type diagnosticWirePosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func rpcIDToInt(id any) (int64, bool) {
	switch value := id.(type) {
	case float64:
		return int64(value), true
	case int64:
		return value, true
	case int:
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func severityLabel(value int) string {
	switch value {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return ""
	}
}

func diagnosticCodeString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func pathToFileURI(path string) string {
	clean := filepath.Clean(path)
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(clean)}).String()
}
