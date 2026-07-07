package acpdaemon

import (
	"sync"

	"github.com/grigory51/brigade/backend/internal/agui"
)

// permStore — реестр ожидающих permission-запросов демона (human-in-the-loop). Запрос
// регистрируется resolver'ом (он блокируется на канале), решение доставляется ResolvePermission.
// Аналог PermissionStore из transport/agui, но живёт в демоне (там висящие запросы —
// источник истины для reconnect: brigade на переподключении переспрашивает Pending).
type permStore struct {
	mu      sync.Mutex
	pending map[string]*pendingPerm
}

type pendingPerm struct {
	req agui.PermissionRequest
	ch  chan string // сюда доставляется решение (OptionID) либо "" (отмена)
}

func newPermStore() *permStore {
	return &permStore{pending: make(map[string]*pendingPerm)}
}

// register регистрирует ожидание по req.ID и возвращает канал, на котором resolver ждёт
// решение. Буфер 1 — доставка не блокирует Deliver, даже если resolver уже ушёл по ctx.
func (p *permStore) register(req agui.PermissionRequest) <-chan string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ch := make(chan string, 1)
	p.pending[req.ID] = &pendingPerm{req: req, ch: ch}
	return ch
}

// deliver доставляет решение ожидающему запросу по id (no-op, если ожидания нет).
func (p *permStore) deliver(id, decision string) {
	p.mu.Lock()
	pp, ok := p.pending[id]
	if ok {
		delete(p.pending, id)
	}
	p.mu.Unlock()
	if ok {
		pp.ch <- decision
	}
}

// remove снимает ожидание без доставки (resolver ушёл по ctx).
func (p *permStore) remove(id string) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

// pendingList — снимок висящих запросов (для повторной выдачи на reconnect).
func (p *permStore) pendingList() []agui.PermissionRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]agui.PermissionRequest, 0, len(p.pending))
	for _, pp := range p.pending {
		out = append(out, pp.req)
	}
	return out
}

// cancelAll сворачивает все висящие запросы пустым решением (отмена turn'а).
func (p *permStore) cancelAll() {
	p.mu.Lock()
	all := p.pending
	p.pending = make(map[string]*pendingPerm)
	p.mu.Unlock()
	for _, pp := range all {
		pp.ch <- ""
	}
}
