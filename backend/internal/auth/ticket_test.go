package auth

import (
	"testing"
	"time"
)

// TestTicketIssueRedeem проверяет базовый сценарий: выпущенный тикет погашается
// один раз для правильной сессии и больше не действителен (одноразовость).
func TestTicketIssueRedeem(t *testing.T) {
	ts := NewTicketStore()

	token, err := ts.Issue("user-1", "session-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	userID, ok := ts.Redeem(token, "session-1")
	if !ok {
		t.Fatal("Redeem: ok = false, want true")
	}
	if userID != "user-1" {
		t.Errorf("userID = %q, want %q", userID, "user-1")
	}

	// Повторное погашение того же тикета должно провалиться.
	if _, ok := ts.Redeem(token, "session-1"); ok {
		t.Error("повторный Redeem: ok = true, want false")
	}
}

// TestTicketWrongSession проверяет привязку тикета к session_id: предъявление
// для чужой сессии отклоняется, но не «сжигает» валидный тикет.
func TestTicketWrongSession(t *testing.T) {
	ts := NewTicketStore()

	token, err := ts.Issue("user-1", "session-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, ok := ts.Redeem(token, "session-2"); ok {
		t.Fatal("Redeem чужой сессии: ok = true, want false")
	}

	// Тикет должен пережить неудачную попытку и сработать для своей сессии.
	if _, ok := ts.Redeem(token, "session-1"); !ok {
		t.Fatal("Redeem правильной сессии после промаха: ok = false, want true")
	}
}

// TestTicketExpired проверяет, что истёкший тикет не погашается.
func TestTicketExpired(t *testing.T) {
	ts := NewTicketStore()
	base := time.Unix(1_700_000_000, 0)
	ts.now = func() time.Time { return base }

	token, err := ts.Issue("user-1", "session-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Сдвигаем «текущее время» за пределы TTL.
	ts.now = func() time.Time { return base.Add(ticketTTL + time.Second) }

	if _, ok := ts.Redeem(token, "session-1"); ok {
		t.Error("Redeem истёкшего: ok = true, want false")
	}
}

// TestTicketEvictsExpiredOnIssue проверяет, что Issue вычищает истёкшие тикеты,
// не давая карте расти при неиспользуемых выдачах.
func TestTicketEvictsExpiredOnIssue(t *testing.T) {
	ts := NewTicketStore()
	base := time.Unix(1_700_000_000, 0)
	ts.now = func() time.Time { return base }

	if _, err := ts.Issue("user-1", "session-1"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Время ушло за TTL первого тикета; следующая выдача должна его вытеснить.
	ts.now = func() time.Time { return base.Add(ticketTTL + time.Second) }
	if _, err := ts.Issue("user-2", "session-2"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if got := len(ts.tickets); got != 1 {
		t.Errorf("после eviction в карте %d тикетов, want 1", got)
	}
}

// TestTicketEmptyToken проверяет, что пустой токен сразу отклоняется.
func TestTicketEmptyToken(t *testing.T) {
	ts := NewTicketStore()
	if _, ok := ts.Redeem("", "session-1"); ok {
		t.Error("Redeem пустого токена: ok = true, want false")
	}
}
