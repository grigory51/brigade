package connectsvc

import (
	"encoding/json"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	aguimodel "github.com/grigory51/brigade/backend/internal/agui"
	"github.com/grigory51/brigade/backend/internal/transport/agui"
)

// TestConfigOptionsToProto проверяет нормализацию опций: Select с ungrouped и grouped
// значениями сплющивается в единый плоский список (grouped не теряется); Boolean-опции
// пропускаются.
func TestConfigOptionsToProto(t *testing.T) {
	cat := acpsdk.SessionConfigOptionCategory("model")
	desc := "efficient"
	ungrouped := acpsdk.SessionConfigSelectOptionsUngrouped{
		{Value: "sonnet", Name: "Sonnet", Description: &desc},
	}
	grouped := acpsdk.SessionConfigSelectOptionsGrouped{
		{Group: "anthropic", Name: "Anthropic", Options: []acpsdk.SessionConfigSelectOption{
			{Value: "opus", Name: "Opus"},
			{Value: "haiku", Name: "Haiku"},
		}},
	}
	opts := []acpsdk.SessionConfigOption{
		{Select: &acpsdk.SessionConfigOptionSelect{
			Id: "model", Name: "Model", Category: &cat, CurrentValue: "opus",
			Options: acpsdk.SessionConfigSelectOptions{Ungrouped: &ungrouped},
		}},
		{Select: &acpsdk.SessionConfigOptionSelect{
			Id: "grouped", Name: "Grouped", CurrentValue: "opus",
			Options: acpsdk.SessionConfigSelectOptions{Grouped: &grouped},
		}},
		{Boolean: &acpsdk.SessionConfigOptionBoolean{}}, // пропускается
	}

	got := configOptionsToProto(opts)
	if len(got) != 2 {
		t.Fatalf("опций = %d, want 2 (Boolean пропущен)", len(got))
	}
	if got[0].Id != "model" || got[0].Category != "model" || len(got[0].Options) != 1 {
		t.Errorf("ungrouped опция = %+v", got[0])
	}
	if got[0].Options[0].Value != "sonnet" || got[0].Options[0].Description != "efficient" {
		t.Errorf("ungrouped значение = %+v", got[0].Options[0])
	}
	// Grouped сплющена: 2 значения из группы, без заголовка.
	if len(got[1].Options) != 2 || got[1].Options[0].Value != "opus" || got[1].Options[1].Value != "haiku" {
		t.Errorf("grouped не сплющена: %+v", got[1].Options)
	}
}

// TestPendingPermissionsMarshalShape фиксирует контракт GetStatus↔фронт: зарегистрированный в
// PermissionStore запрос отдаётся в JSON с полями, которые парсит клиентский toPermission
// (id/title/options[].optionId/name/kind). По этому JSON фронт восстанавливает диалог
// разрешения после переоткрытия (история грузится unary, CUSTOM permission_request не приходит).
func TestPendingPermissionsMarshalShape(t *testing.T) {
	ps := agui.NewPermissionStore()
	req := aguimodel.PermissionRequest{
		ID: "p1", Title: "Run curl",
		Options: []aguimodel.PermissionOption{{OptionID: "allow", Name: "Разрешить", Kind: "allow_once"}},
	}
	_, release := ps.Register("t1", "p1", req)
	defer release()

	pend := ps.Pending("t1")
	if len(pend) != 1 {
		t.Fatalf("pending = %d, want 1", len(pend))
	}
	data, err := json.Marshal(pend[0]) // тот же маршалинг, что в AcpService.GetStatus
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["id"] != "p1" || m["title"] != "Run curl" {
		t.Fatalf("форма id/title: %s", data)
	}
	opts, _ := m["options"].([]any)
	if len(opts) != 1 {
		t.Fatalf("options: %s", data)
	}
	o0, _ := opts[0].(map[string]any)
	if o0["optionId"] != "allow" || o0["name"] != "Разрешить" || o0["kind"] != "allow_once" {
		t.Errorf("форма option (optionId/name/kind): %s", data)
	}
}
