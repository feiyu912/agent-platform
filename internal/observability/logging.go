package observability

import (
	"encoding/json"
	"log"
	"regexp"
)

const HiddenToken = "<HIDDEN_TOKEN>"

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9_\-\.=]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret)\s*[:=]\s*)[a-z0-9_\-\.=]+`),
	regexp.MustCompile(`(?i)((?:[?&](?:api[_-]?key|token|secret)=))[^&\s]+`),
	regexp.MustCompile(`(?i)sk-[a-z0-9]+`),
}

func Log(category string, fields map[string]any) {
	payload := map[string]any{"category": category}
	for key, value := range fields {
		payload[key] = value
	}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[obs][%s] marshal_error=%v", category, err)
		return
	}
	log.Printf("%s", data)
}

func SanitizeLog(text string) string {
	sanitized := text
	sanitized = sensitivePatterns[0].ReplaceAllString(sanitized, "${1}"+HiddenToken)
	sanitized = sensitivePatterns[1].ReplaceAllString(sanitized, "${1}"+HiddenToken)
	sanitized = sensitivePatterns[2].ReplaceAllString(sanitized, "${1}"+HiddenToken)
	sanitized = sensitivePatterns[3].ReplaceAllString(sanitized, HiddenToken)
	return sanitized
}
