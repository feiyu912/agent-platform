package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readJSONLines reads a JSONL file. Uses json.Decoder so it handles both
// single-line JSON objects (Go's writer) and pretty-printed multi-line JSON
// objects (Java may write either format).
func readJSONLines(path string) ([]map[string]any, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var items []map[string]any
	decoder := json.NewDecoder(file)
	for {
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("parse JSONL: %w", err)
		}
		if payload != nil {
			items = append(items, payload)
		}
	}
	return items, nil
}

func (s *FileStore) LoadJSONLContent(chatID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chatID = strings.TrimSpace(chatID)
	if !ValidChatID(chatID) {
		return "", os.ErrPermission
	}
	summary, err := s.loadSummary(chatID)
	if err != nil {
		return "", err
	}
	if summary == nil {
		return "", ErrChatNotFound
	}
	return readFileStringIfExists(s.chatJSONLPath(chatID))
}
