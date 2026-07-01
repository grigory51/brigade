// Package a2ui описывает сообщения протокола A2UI (Agent-to-User Interface,
// specification v0.9) как Go-структуры для сериализации в JSON.
//
// A2UI — декларативный формат generative UI: сервер описывает поверхность (surface),
// дерево компонентов и модель данных, клиентский рендерер (например, @a2ui/react)
// отрисовывает их нативными виджетами. brigade выступает генератором: ACP-агент
// присылает текст и tool call'ы, а не A2UI, поэтому surfaces для семантических карточек
// синтезирует бэкенд (см. internal/acp — трансляция diff tool call'а).
//
// Официального Go SDK у A2UI нет (agent_sdks — python/kotlin); подмножество ниже
// покрывает четыре server→client сообщения спецификации v0.9. Имена полей соответствуют
// specification/v0_9/json/server_to_client.json.
package a2ui

// Version — версия протокола в каждом сообщении.
const Version = "v0.9"

// Message — одно server→client сообщение A2UI: конверт с ровно одним заполненным
// полем-операцией (createSurface | updateComponents | updateDataModel | deleteSurface).
type Message struct {
	Version string `json:"version"`

	CreateSurface    *CreateSurface    `json:"createSurface,omitempty"`
	UpdateComponents *UpdateComponents `json:"updateComponents,omitempty"`
	UpdateDataModel  *UpdateDataModel  `json:"updateDataModel,omitempty"`
	DeleteSurface    *DeleteSurface    `json:"deleteSurface,omitempty"`
}

// CreateSurface инициализирует поверхность рендера с указанным каталогом компонентов.
type CreateSurface struct {
	SurfaceID string `json:"surfaceId"`
	CatalogID string `json:"catalogId"`
}

// UpdateComponents задаёт дерево компонентов поверхности. Компонент — объект с полями
// id, component (имя из каталога) и свойствами компонента; свойства могут быть
// литералами или биндингами вида {"path": "/field"} в модель данных. Свободная форма
// (map) выбрана сознательно: состав свойств определяется каталогом, а не протоколом.
type UpdateComponents struct {
	SurfaceID  string           `json:"surfaceId"`
	Components []map[string]any `json:"components"`
}

// UpdateDataModel записывает значение в модель данных поверхности по JSON-pointer пути.
type UpdateDataModel struct {
	SurfaceID string `json:"surfaceId"`
	Path      string `json:"path"`
	Value     any    `json:"value"`
}

// DeleteSurface удаляет поверхность.
type DeleteSurface struct {
	SurfaceID string `json:"surfaceId"`
}
