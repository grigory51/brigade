package acpremote

import (
	"testing"

	"github.com/grigory51/brigade/backend/internal/agui"
)

func TestNormalizeLegacyDaemonText(t *testing.T) {
	events := normalizeDaemonEvent(agui.Event{Type: agui.EventTextMessageContent, Delta: "error"})
	if len(events) != 3 || events[0].Type != agui.EventTextMessageStart || events[1].Type != agui.EventTextMessageContent || events[2].Type != agui.EventTextMessageEnd {
		t.Fatalf("events = %+v", events)
	}
	if events[0].MessageID == "" || events[1].MessageID != events[0].MessageID || events[2].MessageID != events[0].MessageID {
		t.Fatalf("message ids = %q, %q, %q", events[0].MessageID, events[1].MessageID, events[2].MessageID)
	}
}

func TestSessionTitleEventUpdatesClientWithoutSink(t *testing.T) {
	var got string
	c := &Client{OnSessionTitle: func(title string) { got = title }}
	c.updateSessionTitle(agui.Event{
		Type:  agui.EventCustom,
		Name:  agui.CustomSessionTitleName,
		Value: map[string]any{"title": "Новая тема"},
	})
	if got != "Новая тема" || c.sessionTitle != got {
		t.Fatalf("callback=%q sessionTitle=%q", got, c.sessionTitle)
	}
}
