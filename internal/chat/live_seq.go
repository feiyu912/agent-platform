package chat

import "agent-platform/internal/stream"

func eventPayloadWithoutSeq(event stream.EventData) map[string]any {
	payload := event.Map()
	clearReplayCursorFields(payload)
	return payload
}

func addReplayLiveSeq(payload map[string]any, liveSeq int64) {
	if payload == nil || liveSeq <= 0 {
		return
	}
	payload["liveSeq"] = liveSeq
}

func maxLiveSeq(values ...int64) int64 {
	var max int64
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func clearReplayCursorFields(payload map[string]any) {
	if payload == nil {
		return
	}
	delete(payload, "seq")
	delete(payload, "liveSeq")
}
