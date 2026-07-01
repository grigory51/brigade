package agui

import "testing"

// TestPermissionDeliverOnce проверяет базовый путь: зарегистрированное ожидание
// получает решение, и второй ответ, пришедший до чтения из канала, отклоняется —
// неблокирующая отправка защищает от повторного ответа на ещё не прочитанное решение.
func TestPermissionDeliverOnce(t *testing.T) {
	p := newPermissionStore()
	ch, release := p.register("k")
	defer release()

	if !p.deliver("k", "allow") {
		t.Fatal("первый deliver: ok = false, want true")
	}

	// Второй ответ приходит, пока решение ещё в буфере (резолвер не прочитал): канал
	// занят, неблокирующая отправка не должна ни блокироваться, ни паниковать — false.
	if p.deliver("k", "reject") {
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
	p := newPermissionStore()
	_, release := p.register("k")
	release()

	if p.deliver("k", "allow") {
		t.Fatal("deliver после release: ok = true, want false")
	}
}

// TestPermissionDeliverUnknownKey проверяет, что доставка незарегистрированного ключа
// (опоздавший/повторный ответ) — не ошибка и не паника, просто false.
func TestPermissionDeliverUnknownKey(t *testing.T) {
	p := newPermissionStore()
	if p.deliver("missing", "allow") {
		t.Fatal("deliver незарегистрированного ключа: ok = true, want false")
	}
}
