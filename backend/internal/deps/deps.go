// Package deps фиксирует прямые зависимости, которые потребуются последующим шагам
// (JWT/bcrypt), но ещё не импортируются доменным кодом. Без явной ссылки `go mod tidy`
// исключил бы их из require-блока. Файл удаляется по мере того, как пакеты начинают
// использоваться в реальном коде.
//
// pty (creack/pty) и docker/client задействованы в internal/spawn; websocket и
// acp-go-sdk — в internal/transport и internal/acp, поэтому здесь не перечислены.
// golang-jwt и bcrypt остаются в blank-import до реализации auth.
package deps

import (
	// Auth: подпись JWT и проверка пароля.
	_ "github.com/golang-jwt/jwt/v5"
	_ "golang.org/x/crypto/bcrypt"
)
