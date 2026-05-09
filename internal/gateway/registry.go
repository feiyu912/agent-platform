// Package gateway 管理多条反向 WS 连接（wecom / feishu / ding / ...），
// 每条对应一个 channel 插件。Registry 是 connector 的索引：
//   - 按 ID 做增删查（Admin API 场景）
//   - 按 Channel 做路由（artifact 外推、文件下载按 chatId 前缀选择 gateway）
//
// legacy 单 gateway 部署是"一条 channel 空串的 entry"的特例，路由退化为总命中它。
//
// Registry 本身不决定 gateway 生命周期——StartAll 从 config 初始化首批，
// Admin API 之后运行时可 Register / Unregister。
package gateway

import (
	"context"
	"fmt"
	"log"
	neturl "net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"agent-platform-runner-go/internal/channel"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/ws"
	"agent-platform-runner-go/internal/ws/gatewayclient"
)

// Entry 是 Registry 里一条 gateway 的运行态快照，给 Admin API List 用。
type Entry struct {
	ID            string
	Channel       string
	SourceChannel string
	SourcePrefix  string
	URL           string
	BaseURL       string
	Token         string // 返回给调用方时一般不暴露，仅内部使用
	client        *gatewayclient.Client
}

// Registry 线程安全。
type Registry struct {
	mu              sync.RWMutex
	entries         map[string]*Entry // id → entry
	byChannel       map[string]string // user channel id → id
	bySourceChannel map[string]string // full source channel (wecom:xiaozhai) → id
	bySourcePrefix  map[string]string // source/chatId prefix → id only when unambiguous

	// 依赖：新建 connector 时需要这些
	wsCfg     config.WebSocketConfig
	heartbeat time.Duration
	hub       *ws.Hub
	dispatch  ws.RouteHandler

	rootCtx context.Context
}

// New 创建 Registry。rootCtx 用于给每个 connector.Start 传递；Registry 自己不起 goroutine。
func New(rootCtx context.Context, wsCfg config.WebSocketConfig, heartbeat time.Duration, hub *ws.Hub, dispatch ws.RouteHandler) *Registry {
	return &Registry{
		entries:         map[string]*Entry{},
		byChannel:       map[string]string{},
		bySourceChannel: map[string]string{},
		bySourcePrefix:  map[string]string{},
		wsCfg:           wsCfg,
		heartbeat:       heartbeat,
		hub:             hub,
		dispatch:        dispatch,
		rootCtx:         rootCtx,
	}
}

// Register 启动一个 gateway connector 并加入 Registry。
// 若 id 已存在返回 ErrDuplicateID；URL/Token 为空返回 ErrInvalidConfig。
func (r *Registry) Register(entry config.GatewayEntry) error {
	id := strings.TrimSpace(entry.ID)
	if id == "" {
		return ErrInvalidConfig
	}
	if strings.TrimSpace(entry.URL) == "" {
		return ErrInvalidConfig
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[id]; exists {
		return ErrDuplicateID
	}
	channelKey := strings.TrimSpace(entry.Channel)
	sourceChannel := strings.TrimSpace(entry.SourceChannel)
	if sourceChannel == "" {
		sourceChannel = deriveSourceChannelFromURL(entry.URL)
	}
	sourcePrefix := strings.TrimSpace(entry.SourcePrefix)
	if sourcePrefix == "" {
		sourcePrefix = sourcePrefixFromChannel(sourceChannel)
	}
	if channelKey != "" {
		if _, exists := r.byChannel[channelKey]; exists {
			return ErrDuplicateChannel
		}
	}
	if sourceChannel != "" {
		if _, exists := r.bySourceChannel[sourceChannel]; exists {
			return ErrDuplicateChannel
		}
	}

	client := gatewayclient.New(
		gatewayclient.Config{
			ID:               id,
			Channel:          channelKey,
			URL:              strings.TrimSpace(entry.URL),
			BaseURL:          strings.TrimSpace(entry.BaseURL),
			Token:            strings.TrimSpace(entry.JwtToken),
			HandshakeTimeout: time.Duration(entry.HandshakeTimeoutMs) * time.Millisecond,
			ReconnectMin:     time.Duration(entry.ReconnectMinMs) * time.Millisecond,
			ReconnectMax:     time.Duration(entry.ReconnectMaxMs) * time.Millisecond,
		},
		r.wsCfg,
		r.heartbeat,
		r.hub,
		r.dispatch,
	)
	client.Start(r.rootCtx)

	e := &Entry{
		ID:            id,
		Channel:       channelKey,
		SourceChannel: sourceChannel,
		SourcePrefix:  sourcePrefix,
		URL:           strings.TrimSpace(entry.URL),
		BaseURL:       strings.TrimSpace(entry.BaseURL),
		Token:         strings.TrimSpace(entry.JwtToken),
		client:        client,
	}
	r.entries[id] = e
	if e.Channel != "" {
		r.byChannel[e.Channel] = id
	}
	if e.SourceChannel != "" {
		r.bySourceChannel[e.SourceChannel] = id
	}
	r.rebuildSourcePrefixIndexLocked()
	return nil
}

// Unregister 停止 connector 并从 Registry 移除。不存在时返回 ErrNotFound。
func (r *Registry) Unregister(id string) error {
	r.mu.Lock()
	entry, ok := r.entries[id]
	if !ok {
		r.mu.Unlock()
		return ErrNotFound
	}
	delete(r.entries, id)
	if entry.Channel != "" && r.byChannel[entry.Channel] == id {
		delete(r.byChannel, entry.Channel)
	}
	if entry.SourceChannel != "" && r.bySourceChannel[entry.SourceChannel] == id {
		delete(r.bySourceChannel, entry.SourceChannel)
	}
	r.rebuildSourcePrefixIndexLocked()
	r.mu.Unlock()

	return entry.client.Stop()
}

// LookupByID 查 entry；主要给 Admin API 和测试用。
func (r *Registry) LookupByID(id string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[id]
	return e, ok
}

// LookupByChatID 按 chatId 前缀（例如 "wecom#..." → channel "wecom"）查 entry。
// 路由策略：
//  1. 提取 chatId 第一个 '#' 前的 channel 前缀
//  2. Registry 里有匹配 channel 的 entry 就返回
//  3. 没匹配到时，若 Registry 只有一条 entry（典型 legacy 单 gateway），返回它作为兜底
//  4. 多条 entry 且无匹配则返回 nil，false
func (r *Registry) LookupByChatID(chatID string) (*Entry, bool) {
	channelID := channel.ChannelForChatID(chatID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if channelID != "" {
		if id, ok := r.byChannel[channelID]; ok {
			return r.entries[id], true
		}
		if id, ok := r.bySourcePrefix[channelID]; ok {
			return r.entries[id], true
		}
	}
	if len(r.entries) == 1 {
		for _, e := range r.entries {
			return e, true
		}
	}
	return nil, false
}

func (r *Registry) LookupBySourceChannel(sourceChannel string) (*Entry, bool) {
	sourceChannel = strings.TrimSpace(sourceChannel)
	if sourceChannel == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.bySourceChannel[sourceChannel]; ok {
		return r.entries[id], true
	}
	return nil, false
}

// Resolver 是按 chatId 查 gateway 的只读视图。artifactpusher / ws_routes 通过它解耦 Registry 内部。
type Resolver interface {
	Resolve(chatID string) (baseURL string, token string, ok bool)
}

// Resolve 按 chatId 前缀查对应 gateway 的 BaseURL 和 Token（路由 artifact 外推 / 文件下载）。
// 查不到时 ok=false，调用方按"无对应 gateway"处理（pusher 跳过，download 返回错误）。
func (r *Registry) Resolve(chatID string) (string, string, bool) {
	entry, ok := r.LookupByChatID(chatID)
	if !ok {
		return "", "", false
	}
	return entry.BaseURL, entry.Token, true
}

func (r *Registry) ResolveSourceChannel(sourceChannel string) (string, string, bool) {
	entry, ok := r.LookupBySourceChannel(sourceChannel)
	if !ok {
		return "", "", false
	}
	return entry.BaseURL, entry.Token, true
}

func (r *Registry) rebuildSourcePrefixIndexLocked() {
	counts := map[string]int{}
	owner := map[string]string{}
	for id, entry := range r.entries {
		prefix := strings.TrimSpace(entry.SourcePrefix)
		if prefix == "" {
			prefix = sourcePrefixFromChannel(entry.SourceChannel)
		}
		if prefix == "" {
			continue
		}
		counts[prefix]++
		owner[prefix] = id
	}
	r.bySourcePrefix = map[string]string{}
	for prefix, count := range counts {
		if count == 1 {
			r.bySourcePrefix[prefix] = owner[prefix]
		}
	}
}

func sourcePrefixFromChannel(sourceChannel string) string {
	sourceChannel = strings.TrimSpace(sourceChannel)
	if sourceChannel == "" {
		return ""
	}
	if idx := strings.Index(sourceChannel, ":"); idx > 0 {
		return sourceChannel[:idx]
	}
	return sourceChannel
}

func deriveSourceChannelFromURL(raw string) string {
	parsed, err := neturl.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return ""
	}
	return strings.TrimSpace(parsed.Query().Get("channel"))
}

// Connected 返回指定 channel 当前对应的 gateway 反向 WS 是否在线。
func (r *Registry) Connected(channelID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byChannel[strings.TrimSpace(channelID)]
	if !ok {
		return false
	}
	entry, ok := r.entries[id]
	if !ok || entry == nil || entry.client == nil {
		return false
	}
	return entry.client.Connected()
}

// All 返回当前所有 entries 的快照。调用方不应修改 slice 元素。
func (r *Registry) All() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	return out
}

// StopAll 停所有 connector；通常只在 App.Close 调用。
func (r *Registry) StopAll() {
	r.mu.Lock()
	entries := r.entries
	r.entries = map[string]*Entry{}
	r.byChannel = map[string]string{}
	r.bySourceChannel = map[string]string{}
	r.bySourcePrefix = map[string]string{}
	r.mu.Unlock()
	for _, e := range entries {
		_ = e.client.Stop()
	}
}

// Reconcile 把当前注册表对齐到 desired 列表：
//   - desired 里有、注册表里没 → Register
//   - 注册表里有、desired 里没 → Unregister 并关闭连接
//   - 双方都有但内容变化（任意字段不同）→ Unregister 旧的 + Register 新的
func (r *Registry) Reconcile(desired []config.GatewayEntry) error {
	r.mu.Lock()
	currentIDs := make(map[string]*Entry, len(r.entries))
	for id, e := range r.entries {
		currentIDs[id] = e
	}
	r.mu.Unlock()

	desiredIDs := make(map[string]config.GatewayEntry, len(desired))
	for _, entry := range desired {
		desiredIDs[entry.ID] = entry
	}

	// Unregister entries that are no longer desired
	for id := range currentIDs {
		if _, stillDesired := desiredIDs[id]; !stillDesired {
			if err := r.Unregister(id); err != nil {
				log.Printf("[gateway] reconcile: unregister %s failed: %v", id, err)
			} else {
				log.Printf("[gateway] reconcile: unregistered %s", id)
			}
		}
	}

	// Register or update entries
	for _, entry := range desired {
		id := entry.ID
		current, exists := currentIDs[id]
		if !exists {
			// New entry, register it
			if err := r.Register(entry); err != nil {
				log.Printf("[gateway] reconcile: register %s failed: %v", id, err)
			} else {
				log.Printf("[gateway] reconcile: registered %s", id)
			}
			continue
		}

		// Entry exists, check if any relevant field changed
		// Only compare fields stored in Entry (Channel, URL, BaseURL, Token).
		// HandshakeTimeoutMs/ReconnectMinMs/ReconnectMaxMs are used at registration only.
		expectedEntry := config.GatewayEntry{
			ID:            id,
			Channel:       strings.TrimSpace(entry.Channel),
			SourceChannel: strings.TrimSpace(entry.SourceChannel),
			SourcePrefix:  strings.TrimSpace(entry.SourcePrefix),
			URL:           strings.TrimSpace(entry.URL),
			BaseURL:       strings.TrimSpace(entry.BaseURL),
		}
		actualEntry := config.GatewayEntry{
			ID:            current.ID,
			Channel:       current.Channel,
			SourceChannel: current.SourceChannel,
			SourcePrefix:  current.SourcePrefix,
			URL:           current.URL,
			BaseURL:       current.BaseURL,
		}
		if !reflect.DeepEqual(expectedEntry, actualEntry) ||
			strings.TrimSpace(entry.JwtToken) != current.Token {
			// Changed, unregister old and register new
			if err := r.Unregister(id); err != nil {
				log.Printf("[gateway] reconcile: unregister %s (update) failed: %v", id, err)
				continue
			}
			log.Printf("[gateway] reconcile: %s changed, re-registering", id)
			if err := r.Register(entry); err != nil {
				log.Printf("[gateway] reconcile: re-register %s failed: %v", id, err)
			} else {
				log.Printf("[gateway] reconcile: re-registered %s", id)
			}
		}
	}

	return nil
}

var (
	ErrDuplicateID      = fmt.Errorf("gateway: duplicate id")
	ErrDuplicateChannel = fmt.Errorf("gateway: duplicate channel")
	ErrNotFound         = fmt.Errorf("gateway: id not found")
	ErrInvalidConfig    = fmt.Errorf("gateway: invalid config (id/url required)")
)
