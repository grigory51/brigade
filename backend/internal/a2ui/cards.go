package a2ui

// CardsCatalogID — идентификатор каталога карточек brigade. Клиентские рендереры
// (web: @a2ui/react-каталог в web/src/features/acp/a2ui, mobile: Compose-каталог)
// реализуют одни и те же компоненты под этим идентификатором — сервер описывает
// карточку один раз, платформы рендерят нативно.
const CardsCatalogID = "https://brigade.dev/a2ui/catalogs/cards/v1"

// DiffData — модель данных карточки правки файла (компонент DiffView каталога).
type DiffData struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// DiffSurface собирает полную поставку поверхности с карточкой diff'а: создание
// поверхности, дерево из одного компонента DiffView с биндингами и модель данных.
// surfaceID соответствует toolCallId — клиент по нему находит поверхность при рендере
// tool call'а.
func DiffSurface(surfaceID string, diffs []DiffData) []Message {
	return []Message{
		{Version: Version, CreateSurface: &CreateSurface{
			SurfaceID: surfaceID,
			CatalogID: CardsCatalogID,
		}},
		{Version: Version, UpdateComponents: &UpdateComponents{
			SurfaceID: surfaceID,
			Components: []map[string]any{{
				"id":        "root",
				"component": "DiffView",
				"diffs":     map[string]any{"path": "/diffs"},
			}},
		}},
		{Version: Version, UpdateDataModel: &UpdateDataModel{
			SurfaceID: surfaceID,
			Path:      "/",
			Value:     map[string]any{"diffs": diffs},
		}},
	}
}
