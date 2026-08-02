package store

import (
	"context"
	"testing"
)

func TestNotificationBackendsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	first := NotificationBackend{
		ID: "n1", UserID: "u1", Kind: "ntfy", Name: "phone",
		Config: `{"topic":"one"}`, Secret: "token", Events: "turn_end",
	}
	if err := st.SaveNotificationBackend(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveNotificationBackend(ctx, NotificationBackend{
		ID: "n2", UserID: "u1", Kind: "ntfy", Name: "desktop",
		Config: `{"topic":"two"}`, Events: "error",
	}); err != nil {
		t.Fatal(err)
	}
	first.Name = "work phone"
	first.Secret = "" // пустой секрет не стирает сохранённый
	if err := st.SaveNotificationBackend(ctx, first); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListNotificationBackends(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "work phone" || got[0].Secret != "token" {
		t.Fatalf("unexpected backends: %+v", got)
	}
}
