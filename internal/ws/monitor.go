package ws

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"agent-platform/internal/observability"
)

const (
	monitorConnectionCapacity = 500
	monitorMessageCapacity    = 100
	monitorPreviewMaxRunes    = 512
	monitorUserAgentMaxRunes  = 200
)

var monitorSensitiveJSONField = regexp.MustCompile(`(?i)("[^"]*(?:api[_-]?key|token|secret|authorization)[^"]*"\s*:\s*")([^"]*)(")`)

type MonitorOverview struct {
	GeneratedAt int64            `json:"generatedAt"`
	WS          MonitorWSSummary `json:"ws"`
}

type MonitorWSSummary struct {
	ConnectionCount  int                `json:"connectionCount"`
	LatestConnection *MonitorConnection `json:"latestConnection"`
	RecentMessages   []MonitorMessage   `json:"recentMessages"`
}

type MonitorConnectionsSnapshot struct {
	GeneratedAt     int64               `json:"generatedAt"`
	ConnectionCount int                 `json:"connectionCount"`
	Connections     []MonitorConnection `json:"connections"`
}

type MonitorMessagesSnapshot struct {
	GeneratedAt int64            `json:"generatedAt"`
	Messages    []MonitorMessage `json:"messages"`
}

type MonitorConnection struct {
	SessionID        string `json:"sessionId"`
	Kind             string `json:"kind"`
	Active           bool   `json:"active"`
	Subject          string `json:"subject"`
	GatewayID        string `json:"gatewayId"`
	Channel          string `json:"channel"`
	RemoteAddr       string `json:"remoteAddr"`
	UserAgent        string `json:"userAgent"`
	ConnectedAt      int64  `json:"connectedAt"`
	ClosedAt         int64  `json:"closedAt"`
	LastSeenAt       int64  `json:"lastSeenAt"`
	LastMessageAt    int64  `json:"lastMessageAt"`
	ReceivedMessages int64  `json:"receivedMessages"`
	SentMessages     int64  `json:"sentMessages"`
	Errors           int64  `json:"errors"`
	InflightRequests int    `json:"inflightRequests"`
	ActiveStreams    int    `json:"activeStreams"`
	WriteQueueDepth  int    `json:"writeQueueDepth"`
}

type MonitorMessage struct {
	Seq            int64  `json:"seq"`
	Timestamp      int64  `json:"timestamp"`
	SessionID      string `json:"sessionId"`
	Direction      string `json:"direction"`
	Frame          string `json:"frame"`
	Type           string `json:"type"`
	ID             string `json:"id"`
	SizeBytes      int    `json:"sizeBytes"`
	PayloadPreview string `json:"payloadPreview"`
	Truncated      bool   `json:"truncated"`
	Error          string `json:"error"`
}

type monitorConnectionState struct {
	SessionID        string
	Kind             string
	Active           bool
	Subject          string
	GatewayID        string
	Channel          string
	RemoteAddr       string
	UserAgent        string
	ConnectedAt      int64
	ClosedAt         int64
	LastSeenAt       int64
	LastMessageAt    int64
	ReceivedMessages int64
	SentMessages     int64
	Errors           int64
	ConnectedSeq     int64
}

type monitorRuntimeDetails struct {
	InflightRequests int
	ActiveStreams    int
	WriteQueueDepth  int
}

func (h *Hub) MonitorOverview(messageLimit int) MonitorOverview {
	generatedAt := time.Now().UnixMilli()
	connectionCount, connections := h.monitorConnectionSnapshots(1, "")
	var latest *MonitorConnection
	if len(connections) > 0 {
		latestCopy := connections[0]
		latest = &latestCopy
	}
	return MonitorOverview{
		GeneratedAt: generatedAt,
		WS: MonitorWSSummary{
			ConnectionCount:  connectionCount,
			LatestConnection: latest,
			RecentMessages:   h.monitorMessageSnapshots(normalizeMonitorLimit(messageLimit, 5, 50), ""),
		},
	}
}

func (h *Hub) MonitorConnections(limit int, sessionID string) MonitorConnectionsSnapshot {
	generatedAt := time.Now().UnixMilli()
	connectionCount, connections := h.monitorConnectionSnapshots(normalizeMonitorLimit(limit, 100, 500), sessionID)
	return MonitorConnectionsSnapshot{
		GeneratedAt:     generatedAt,
		ConnectionCount: connectionCount,
		Connections:     connections,
	}
}

func (h *Hub) MonitorMessages(limit int, sessionID string) MonitorMessagesSnapshot {
	return MonitorMessagesSnapshot{
		GeneratedAt: time.Now().UnixMilli(),
		Messages:    h.monitorMessageSnapshots(normalizeMonitorLimit(limit, 5, 50), sessionID),
	}
}

func (h *Hub) monitorRegister(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	now := time.Now().UnixMilli()
	kind, subject, gatewayID, channel := conn.monitorIdentity()
	remoteAddr, userAgent := conn.monitorClientInfo()

	h.monitorMu.Lock()
	h.monitorConnSeq++
	h.monitorConns[conn.SessionID()] = &monitorConnectionState{
		SessionID:     conn.SessionID(),
		Kind:          kind,
		Active:        true,
		Subject:       monitorSanitizeText(subject, monitorUserAgentMaxRunes),
		GatewayID:     monitorSanitizeText(gatewayID, monitorUserAgentMaxRunes),
		Channel:       monitorSanitizeText(channel, monitorUserAgentMaxRunes),
		RemoteAddr:    monitorSanitizeRemoteAddr(remoteAddr),
		UserAgent:     monitorSanitizeText(userAgent, monitorUserAgentMaxRunes),
		ConnectedAt:   now,
		LastSeenAt:    now,
		ConnectedSeq:  h.monitorConnSeq,
		LastMessageAt: 0,
	}
	h.latestConnectionID = conn.SessionID()
	h.trimMonitorConnectionsLocked()
	h.monitorMu.Unlock()
}

func (h *Hub) monitorClose(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	now := time.Now().UnixMilli()
	sessionID := conn.SessionID()

	h.monitorMu.Lock()
	state := h.monitorConns[sessionID]
	if state == nil {
		state = &monitorConnectionState{SessionID: sessionID, ConnectedAt: now, ConnectedSeq: h.monitorConnSeq}
		h.monitorConns[sessionID] = state
	}
	state.Active = false
	state.ClosedAt = now
	state.LastSeenAt = now
	h.trimMonitorConnectionsLocked()
	h.monitorMu.Unlock()
}

func (h *Hub) recordMonitorMessage(msg MonitorMessage) {
	if h == nil {
		return
	}
	if msg.Timestamp == 0 {
		msg.Timestamp = time.Now().UnixMilli()
	}
	msg.Error = monitorSanitizeText(msg.Error, monitorPreviewMaxRunes)

	h.monitorMu.Lock()
	h.monitorSeq++
	msg.Seq = h.monitorSeq
	h.monitorMessages = append(h.monitorMessages, msg)
	if overflow := len(h.monitorMessages) - monitorMessageCapacity; overflow > 0 {
		h.monitorMessages = append([]MonitorMessage(nil), h.monitorMessages[overflow:]...)
	}

	state := h.monitorConns[msg.SessionID]
	if state == nil {
		state = &monitorConnectionState{SessionID: msg.SessionID, ConnectedAt: msg.Timestamp, LastSeenAt: msg.Timestamp}
		h.monitorConns[msg.SessionID] = state
	}
	state.LastSeenAt = msg.Timestamp
	state.LastMessageAt = msg.Timestamp
	switch msg.Direction {
	case "in":
		state.ReceivedMessages++
	case "out":
		state.SentMessages++
	}
	if msg.Error != "" || msg.Frame == FrameError {
		state.Errors++
	}
	h.monitorMu.Unlock()
}

func (h *Hub) monitorConnectionSnapshots(limit int, sessionID string) (int, []MonitorConnection) {
	if h == nil {
		return 0, []MonitorConnection{}
	}
	runtimeDetails := h.monitorRuntimeDetails()

	h.monitorMu.RLock()
	defer h.monitorMu.RUnlock()

	states := make([]*monitorConnectionState, 0, len(h.monitorConns))
	if sessionID != "" {
		if state := h.monitorConns[sessionID]; state != nil {
			states = append(states, state)
		}
	} else {
		for _, state := range h.monitorConns {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool {
		if states[i].ConnectedSeq == states[j].ConnectedSeq {
			return states[i].ConnectedAt > states[j].ConnectedAt
		}
		return states[i].ConnectedSeq > states[j].ConnectedSeq
	})
	if len(states) > limit {
		states = states[:limit]
	}

	connections := make([]MonitorConnection, 0, len(states))
	for _, state := range states {
		item := state.snapshot()
		if details, ok := runtimeDetails[state.SessionID]; ok {
			item.InflightRequests = details.InflightRequests
			item.ActiveStreams = details.ActiveStreams
			item.WriteQueueDepth = details.WriteQueueDepth
		}
		connections = append(connections, item)
	}
	return len(runtimeDetails), connections
}

func (h *Hub) monitorMessageSnapshots(limit int, sessionID string) []MonitorMessage {
	if h == nil {
		return []MonitorMessage{}
	}
	h.monitorMu.RLock()
	defer h.monitorMu.RUnlock()

	messages := make([]MonitorMessage, 0, limit)
	for i := len(h.monitorMessages) - 1; i >= 0 && len(messages) < limit; i-- {
		msg := h.monitorMessages[i]
		if sessionID != "" && msg.SessionID != sessionID {
			continue
		}
		messages = append(messages, msg)
	}
	return messages
}

func (h *Hub) monitorRuntimeDetails() map[string]monitorRuntimeDetails {
	details := map[string]monitorRuntimeDetails{}
	if h == nil {
		return details
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for conn := range h.conns {
		if conn == nil {
			continue
		}
		details[conn.SessionID()] = conn.monitorRuntimeDetails()
	}
	return details
}

func (h *Hub) trimMonitorConnectionsLocked() {
	if len(h.monitorConns) <= monitorConnectionCapacity {
		return
	}
	candidates := make([]*monitorConnectionState, 0, len(h.monitorConns))
	for _, state := range h.monitorConns {
		if state == nil || state.Active || state.SessionID == h.latestConnectionID {
			continue
		}
		candidates = append(candidates, state)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ConnectedSeq == candidates[j].ConnectedSeq {
			return candidates[i].ConnectedAt < candidates[j].ConnectedAt
		}
		return candidates[i].ConnectedSeq < candidates[j].ConnectedSeq
	})
	for len(h.monitorConns) > monitorConnectionCapacity && len(candidates) > 0 {
		oldest := candidates[0]
		candidates = candidates[1:]
		delete(h.monitorConns, oldest.SessionID)
	}
}

func (state *monitorConnectionState) snapshot() MonitorConnection {
	if state == nil {
		return MonitorConnection{}
	}
	return MonitorConnection{
		SessionID:        state.SessionID,
		Kind:             state.Kind,
		Active:           state.Active,
		Subject:          state.Subject,
		GatewayID:        state.GatewayID,
		Channel:          state.Channel,
		RemoteAddr:       state.RemoteAddr,
		UserAgent:        state.UserAgent,
		ConnectedAt:      state.ConnectedAt,
		ClosedAt:         state.ClosedAt,
		LastSeenAt:       state.LastSeenAt,
		LastMessageAt:    state.LastMessageAt,
		ReceivedMessages: state.ReceivedMessages,
		SentMessages:     state.SentMessages,
		Errors:           state.Errors,
	}
}

func (c *Conn) SetClientInfo(remoteAddr string, userAgent string) {
	if c == nil {
		return
	}
	c.clientInfoMu.Lock()
	c.remoteAddr = strings.TrimSpace(remoteAddr)
	c.userAgent = strings.TrimSpace(userAgent)
	c.clientInfoMu.Unlock()
}

func (c *Conn) monitorClientInfo() (string, string) {
	if c == nil {
		return "", ""
	}
	c.clientInfoMu.RLock()
	defer c.clientInfoMu.RUnlock()
	return c.remoteAddr, c.userAgent
}

func (c *Conn) monitorIdentity() (string, string, string, string) {
	if c == nil {
		return "client", "", "", ""
	}
	kind := "client"
	if c.silent {
		kind = "gateway"
	}
	c.authMu.RLock()
	auth := c.auth
	c.authMu.RUnlock()
	subject := auth.Subject
	gatewayID := ""
	channel := ""
	if gateway, ok := GatewayFromContext(auth.Context); ok {
		kind = "gateway"
		gatewayID = gateway.ID
		channel = gateway.Channel
	}
	return kind, subject, gatewayID, channel
}

func (c *Conn) monitorRuntimeDetails() monitorRuntimeDetails {
	if c == nil {
		return monitorRuntimeDetails{}
	}
	c.mu.Lock()
	inflightRequests := len(c.inflightRequests)
	activeStreams := len(c.activeStreams)
	c.mu.Unlock()
	writeQueueDepth := 0
	if c.writeQueue != nil {
		writeQueueDepth = len(c.writeQueue)
	}
	return monitorRuntimeDetails{
		InflightRequests: inflightRequests,
		ActiveStreams:    activeStreams,
		WriteQueueDepth:  writeQueueDepth,
	}
}

func (c *Conn) recordInboundMessage(raw []byte, req RequestFrame, errorText string) {
	if c == nil || c.hub == nil || monitorSkipFrame(req.Frame, req.Type) {
		return
	}
	preview, truncated := monitorPayloadPreview(raw)
	c.hub.recordMonitorMessage(MonitorMessage{
		Timestamp:      time.Now().UnixMilli(),
		SessionID:      c.SessionID(),
		Direction:      "in",
		Frame:          req.Frame,
		Type:           req.Type,
		ID:             req.ID,
		SizeBytes:      len(raw),
		PayloadPreview: preview,
		Truncated:      truncated,
		Error:          errorText,
	})
}

func (c *Conn) recordOutboundMessage(frame any) {
	if c == nil || c.hub == nil {
		return
	}
	msg, ok := monitorMessageFromOutboundFrame(c.SessionID(), frame)
	if !ok {
		return
	}
	c.hub.recordMonitorMessage(msg)
}

func monitorMessageFromOutboundFrame(sessionID string, frame any) (MonitorMessage, bool) {
	frameName, frameType, id := monitorFrameMetadata(frame)
	if monitorSkipFrame(frameName, frameType) {
		return MonitorMessage{}, false
	}
	data, err := monitorOutboundPreviewBytes(frame)
	preview, truncated := monitorPayloadPreview(data)
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	if errorFrame, ok := frame.(ErrorFrame); ok && errorFrame.Msg != "" {
		errorText = errorFrame.Msg
	}
	return MonitorMessage{
		Timestamp:      time.Now().UnixMilli(),
		SessionID:      sessionID,
		Direction:      "out",
		Frame:          frameName,
		Type:           frameType,
		ID:             id,
		SizeBytes:      len(data),
		PayloadPreview: preview,
		Truncated:      truncated,
		Error:          errorText,
	}, true
}

func monitorOutboundPreviewBytes(frame any) ([]byte, error) {
	switch value := frame.(type) {
	case ResponseFrame:
		return json.Marshal(struct {
			Frame string `json:"frame"`
			Type  string `json:"type"`
			ID    string `json:"id"`
			Code  int    `json:"code"`
			Msg   string `json:"msg"`
		}{
			Frame: value.Frame,
			Type:  value.Type,
			ID:    value.ID,
			Code:  value.Code,
			Msg:   value.Msg,
		})
	case StreamFrame:
		eventType := ""
		var eventSeq int64
		if value.Event != nil {
			eventType = value.Event.Type
			eventSeq = value.Event.Seq
		}
		return json.Marshal(struct {
			Frame     string `json:"frame"`
			ID        string `json:"id"`
			StreamID  string `json:"streamId"`
			EventType string `json:"eventType,omitempty"`
			EventSeq  int64  `json:"eventSeq,omitempty"`
			Reason    string `json:"reason,omitempty"`
			LastSeq   int64  `json:"lastSeq,omitempty"`
		}{
			Frame:     value.Frame,
			ID:        value.ID,
			StreamID:  value.StreamID,
			EventType: eventType,
			EventSeq:  eventSeq,
			Reason:    value.Reason,
			LastSeq:   value.LastSeq,
		})
	case PushFrame:
		return json.Marshal(struct {
			Frame string `json:"frame"`
			Type  string `json:"type"`
		}{
			Frame: value.Frame,
			Type:  value.Type,
		})
	case ErrorFrame:
		return json.Marshal(struct {
			Frame string `json:"frame"`
			Type  string `json:"type"`
			ID    string `json:"id,omitempty"`
			Code  int    `json:"code"`
			Msg   string `json:"msg"`
		}{
			Frame: value.Frame,
			Type:  value.Type,
			ID:    value.ID,
			Code:  value.Code,
			Msg:   value.Msg,
		})
	default:
		return json.Marshal(frame)
	}
}

func monitorFrameMetadata(frame any) (string, string, string) {
	switch value := frame.(type) {
	case RequestFrame:
		return value.Frame, value.Type, value.ID
	case ResponseFrame:
		return value.Frame, value.Type, value.ID
	case StreamFrame:
		frameType := ""
		if value.Event != nil {
			frameType = value.Event.Type
		} else if value.Reason != "" {
			frameType = "stream." + value.Reason
		}
		return value.Frame, frameType, value.ID
	case PushFrame:
		return value.Frame, value.Type, ""
	case ErrorFrame:
		return value.Frame, value.Type, value.ID
	default:
		return "", "", ""
	}
}

func monitorSkipFrame(frame string, frameType string) bool {
	return frame == FramePush && frameType == "heartbeat"
}

func monitorPayloadPreview(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	return monitorPreviewString(string(data), monitorPreviewMaxRunes)
}

func monitorSanitizeText(text string, maxRunes int) string {
	preview, _ := monitorPreviewString(text, maxRunes)
	return preview
}

func monitorPreviewString(text string, maxRunes int) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	text = observability.SanitizeLog(text)
	text = monitorSensitiveJSONField.ReplaceAllString(text, "${1}"+observability.HiddenToken+"${3}")
	text = strings.ReplaceAll(text, "\r", "\\r")
	text = strings.ReplaceAll(text, "\n", "\\n")
	return monitorTruncate(text, maxRunes)
}

func monitorTruncate(text string, maxRunes int) (string, bool) {
	if maxRunes <= 0 {
		return "", text != ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text, false
	}
	return string(runes[:maxRunes]), true
}

func monitorSanitizeRemoteAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	host := raw
	if parsedHost, _, err := net.SplitHostPort(raw); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("%d.%d.%d.0", ip4[0], ip4[1], ip4[2])
	}
	if ip != nil {
		parts := strings.Split(ip.String(), ":")
		if len(parts) > 4 {
			return strings.Join(parts[:4], ":") + "::"
		}
		return ip.String()
	}
	return monitorSanitizeText(raw, monitorUserAgentMaxRunes)
}

func normalizeMonitorLimit(limit int, fallback int, max int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}
