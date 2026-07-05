package agui

import (
	"testing"

	aguimodel "github.com/grigory51/brigade/backend/internal/agui"
)

func permReq(id string) aguimodel.PermissionRequest {
	return aguimodel.PermissionRequest{ID: id, ToolCallID: id}
}

// TestPermissionDeliverOnce проверяет базовый путь: зарегистрированное ожидание
// получает решение, и второй ответ, пришедший до чтения из канала, отклоняется —
// неблокирующая отправка защищает от повторного ответа на ещё не прочитанное решение.
func TestPermissionDeliverOnce(t *testing.T) {
	p := NewPermissionStore()
	ch, release := p.Register("t", "id", permReq("id"))
	defer release()

	key := PermissionKey("t", "id")
	if !p.Deliver(key, "allow") {
		t.Fatal("первый deliver: ok = false, want true")
	}

	// Второй ответ приходит, пока решение ещё в буфере (резолвер не прочитал): канал
	// занят, неблокирующая отправка не должна ни блокироваться, ни паниковать — false.
	if p.Deliver(key, "reject") {
		t.Fatal("повторный deliver до чтения: ok = true, want false")
	}

	// Читается ровно первое (не перезаписанное) решение.
	if got := <-ch; got != "allow" {
		t.Fatalf("решение = %q, want %q", got, "allow")
	}
}

// TestPermissionDeliverAfterRelease проверяет, что доставка снятого (release) ожидания
// отклоняется — резолвер уже не ждёт ответа.
func TestPermissionDeliverAfterRelease(t *testing.T) {
	p := NewPermissionStore()
	_, release := p.Register("t", "id", permReq("id"))
	release()

	if p.Deliver(PermissionKey("t", "id"), "allow") {
		t.Fatal("deliver после release: ok = true, want false")
	}
}

// TestPermissionDeliverUnknownKey проверяет, что доставка незарегистрированного ключа
// (опоздавший/повторный ответ) — не ошибка и не паника, просто false.
func TestPermissionDeliverUnknownKey(t *testing.T) {
	p := NewPermissionStore()
	if p.Deliver("missing", "allow") {
		t.Fatal("deliver незарегистрированного ключа: ok = true, want false")
	}
}

// TestPermissionPending проверяет, что Pending отдаёт висящие запросы треда (для
// переотправки диалога на reconnect) и что release убирает запрос из списка.
func TestPermissionPending(t *testing.T) {
	p := NewPermissionStore()
	_, r1 := p.Register("t", "a", permReq("a"))
	_, r2 := p.Register("t", "b", permReq("b"))
	_, rOther := p.Register("other", "c", permReq("c"))
	defer rOther()

	if got := len(p.Pending("t")); got != 2 {
		t.Fatalf("Pending(t) = %d, want 2", got)
	}
	if got := len(p.Pending("other")); got != 1 {
		t.Fatalf("Pending(other) = %d, want 1", got)
	}

	r1()
	if got := len(p.Pending("t")); got != 1 {
		t.Fatalf("после release: Pending(t) = %d, want 1", got)
	}
	r2()
	if got := len(p.Pending("t")); got != 0 {
		t.Fatalf("после release обоих: Pending(t) = %d, want 0", got)
	}
}

// TestPermissionCancelPending проверяет надёжный разрыв: CancelPending шлёт пустую строку
// (сигнал отмены) во все висящие запросы треда, не трогая чужие.
func TestPermissionCancelPending(t *testing.T) {
	p := NewPermissionStore()
	ch, release := p.Register("t", "a", permReq("a"))
	defer release()
	chOther, releaseOther := p.Register("other", "b", permReq("b"))
	defer releaseOther()

	p.CancelPending("t")

	if got := <-ch; got != "" {
		t.Fatalf("отмена: решение = %q, want пустую строку", got)
	}
	// Чужой тред не тронут: в канале ничего нет.
	select {
	case v := <-chOther:
		t.Fatalf("чужой тред отменён: получено %q", v)
	default:
	}
}
