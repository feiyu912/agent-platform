package artifactpusher

import "testing"

type recordingNotifications struct {
	eventType string
	data      map[string]any
}

func (r *recordingNotifications) Broadcast(eventType string, data map[string]any) {
	r.eventType = eventType
	r.data = data
}

func TestNotifyArtifactOutgoingUsesResourcePushType(t *testing.T) {
	notifications := &recordingNotifications{}
	pusher := &Pusher{notifications: notifications}

	pusher.notifyArtifactOutgoing("chat-1", "artifact-1", "report.txt", "text/plain", "abc123", 12)

	if notifications.eventType != "resource.push" {
		t.Fatalf("expected resource.push notification, got %q", notifications.eventType)
	}
	if notifications.data["chatId"] != "chat-1" || notifications.data["name"] != "report.txt" || notifications.data["mimeType"] != "text/plain" {
		t.Fatalf("unexpected notification data: %#v", notifications.data)
	}
}
