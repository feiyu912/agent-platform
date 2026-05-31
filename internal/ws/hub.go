package ws

import "sync"

type Hub struct {
	mu    sync.RWMutex
	conns map[*Conn]struct{}

	monitorMu          sync.RWMutex
	monitorConns       map[string]*monitorConnectionState
	latestConnectionID string
	monitorMessages    []MonitorMessage
	monitorSeq         int64
	monitorConnSeq     int64
}

func NewHub() *Hub {
	return &Hub{
		conns:        map[*Conn]struct{}{},
		monitorConns: map[string]*monitorConnectionState{},
	}
}

func (h *Hub) register(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	h.conns[conn] = struct{}{}
	h.mu.Unlock()
	h.monitorRegister(conn)
}

func (h *Hub) unregister(conn *Conn) {
	if h == nil || conn == nil {
		return
	}
	h.mu.Lock()
	delete(h.conns, conn)
	h.mu.Unlock()
	h.monitorClose(conn)
}

func (h *Hub) Broadcast(eventType string, data map[string]any) {
	if h == nil {
		return
	}
	conns := h.snapshotConnections()
	for _, conn := range conns {
		conn.SendPush(eventType, data)
	}
}

func (h *Hub) CloseAll(code int, text string) {
	if h == nil {
		return
	}
	for _, conn := range h.snapshotConnections() {
		conn.close(code, text)
	}
}

func (h *Hub) snapshotConnections() []*Conn {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns := make([]*Conn, 0, len(h.conns))
	for conn := range h.conns {
		conns = append(conns, conn)
	}
	return conns
}
