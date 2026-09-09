package imagebuild

import "context"

type Builder interface {
	// Build вызывает log синхронно и последовательно, только до возврата результата.
	// Допускается nil. Фрагменты сохраняют переводы строк; сообщения состояния
	// и ошибок завершаются переводом строки.
	Build(ctx context.Context, request Request, log func(string)) (Result, error)
}

type Request struct {
	// ID — уникальный идентификатор попытки сборки, назначенный вызывающим сервисом.
	ID      string
	UserID  string
	Script  string
	NoCache bool
}

type Result struct {
	Image   string
	Created bool
}
