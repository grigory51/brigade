package auth

import (
	"sync"
	"time"
)

// ticketTTL — срок жизни WS-тикета. Тикет выдаётся непосредственно перед
// WebSocket-апгрейдом и используется один раз, поэтому окно держится узким.
const ticketTTL = 30 * time.Second

// ticket — связка одноразового тикета с пользователем и сессией.
type ticket struct {
	userID    string
	sessionID string
	expiresAt time.Time
}

// TicketStore — in-memory хранилище одноразовых WS-тикетов с TTL.
//
// Браузерный WebSocket не позволяет задать кастомные заголовки при handshake,
// поэтому access-токен туда не передать. Вместо него аутентифицированный клиент
// получает unary-вызовом короткоживущий тикет, привязанный к конкретной session_id,
// и предъявляет его в query-параметре `?ticket=` при апгрейде. Тикет проверяется
// и сразу инвалидируется (одноразовость).
//
// Хранилище процессное: тикеты эфемерны, переживать рестарт им не нужно.
type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]ticket
	now     func() time.Time
}

// NewTicketStore создаёт пустое хранилище тикетов.
func NewTicketStore() *TicketStore {
	return &TicketStore{
		tickets: make(map[string]ticket),
		now:     time.Now,
	}
}

// Issue выпускает одноразовый тикет для пользователя и сессии.
func (t *TicketStore) Issue(userID, sessionID string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.evictExpiredLocked()
	t.tickets[token] = ticket{
		userID:    userID,
		sessionID: sessionID,
		expiresAt: t.now().Add(ticketTTL),
	}
	return token, nil
}

// Redeem проверяет тикет, привязку к сессии и срок годности, после чего удаляет
// его (одноразовость). Возвращает id пользователя и ok=false при любой ошибке.
func (t *TicketStore) Redeem(token, sessionID string) (userID string, ok bool) {
	if token == "" {
		return "", false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tk, found := t.tickets[token]
	if !found {
		return "", false
	}
	// Тикет инвалидируется только при успешном предъявлении (верная сессия и
	// неистёкший срок). Иначе попытка с неверной session_id «сожгла» бы валидный
	// тикет; несовпавшие тикеты доживают до TTL и вычищаются в evictExpiredLocked.
	if t.now().After(tk.expiresAt) || tk.sessionID != sessionID {
		return "", false
	}
	delete(t.tickets, token)
	return tk.userID, true
}

// evictExpiredLocked удаляет истёкшие тикеты. Вызывается под удержанным mu при
// каждом Issue, чтобы карта не росла неограниченно при неиспользованных тикетах.
func (t *TicketStore) evictExpiredLocked() {
	now := t.now()
	for k, v := range t.tickets {
		if now.After(v.expiresAt) {
			delete(t.tickets, k)
		}
	}
}
