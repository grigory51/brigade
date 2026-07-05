package connectsvc

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
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
