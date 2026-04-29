package server

import (
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
)

func (s *Server) prepareSystemInitCache(req api.QueryRequest, session *contracts.QuerySession, created bool) error {
	if session == nil || s.deps.Chats == nil || s.deps.Tools == nil {
		return nil
	}
	profiles := llm.BuildSystemInitProfiles(*session, req, s.deps.Tools.Definitions())
	if len(profiles) == 0 {
		return nil
	}

	hasAnySystemInit := created
	if !created {
		initLine, err := s.deps.Chats.LoadSystemInit(req.ChatID, "")
		if err != nil {
			return err
		}
		hasAnySystemInit = initLine != nil
	}
	if !hasAnySystemInit {
		session.SystemInitLegacy = true
		return nil
	}

	cache := make(map[string]contracts.SystemInitSnapshot, len(profiles))
	for _, profile := range profiles {
		initLine, err := s.deps.Chats.LoadSystemInit(req.ChatID, profile.CacheKey)
		if err != nil {
			return err
		}
		if initLine != nil && initLine.Fingerprint == profile.Fingerprint {
			cache[profile.CacheKey] = contracts.SystemInitSnapshot{
				Fingerprint:   initLine.Fingerprint,
				SystemMessage: cloneMap(initLine.SystemMessage),
				Tools:         cloneAnySlice(initLine.Tools),
			}
			continue
		}
		line := chat.SystemInitLine{
			Type:          "system-init",
			ChatID:        req.ChatID,
			AgentKey:      session.AgentKey,
			RunID:         session.RunID,
			CreatedAt:     time.Now().UnixMilli(),
			Fingerprint:   profile.Fingerprint,
			CacheKey:      profile.CacheKey,
			Mode:          profile.Mode,
			Stage:         profile.Stage,
			SystemMessage: cloneMap(profile.SystemMessage),
			Tools:         cloneAnySlice(profile.Tools),
		}
		if err := s.deps.Chats.AppendSystemInitLine(req.ChatID, line); err != nil {
			return err
		}
		cache[profile.CacheKey] = contracts.SystemInitSnapshot{
			Fingerprint:   profile.Fingerprint,
			SystemMessage: cloneMap(profile.SystemMessage),
			Tools:         cloneAnySlice(profile.Tools),
		}
	}
	if len(cache) > 0 {
		session.SystemInitCache = cache
	}
	return nil
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneAnySlice(src []any) []any {
	if src == nil {
		return nil
	}
	return append([]any(nil), src...)
}
