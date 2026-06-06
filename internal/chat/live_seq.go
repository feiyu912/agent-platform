package chat

import "agent-platform/internal/stream"

func eventMapWithLiveSeq(event stream.EventData) map[string]any {
	payload := event.Map()
	if event.Seq > 0 {
		payload["liveSeq"] = event.Seq
	}
	delete(payload, "seq")
	return payload
}

func addLiveSeq(payload map[string]any, liveSeq int64) {
	if payload == nil || liveSeq <= 0 {
		return
	}
	payload["liveSeq"] = liveSeq
}
