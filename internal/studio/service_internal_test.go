package studio

import (
	"encoding/json"
	"testing"
)

func TestGuidFromResult(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{`{"value":"11111111-2222-3333-4444-555555555555"}`, "11111111-2222-3333-4444-555555555555"},
		{`{"id":"abc"}`, "abc"},
		{`"bare-guid"`, "bare-guid"},
	} {
		got, err := guidFromResult(json.RawMessage(tc.in))
		if err != nil {
			t.Fatalf("guidFromResult(%s): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("guidFromResult(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := guidFromResult(json.RawMessage(`{}`)); err == nil {
		t.Error("пустой ответ должен быть ошибкой")
	}
}

func TestResourceKey(t *testing.T) {
	// Реальная структура: поле — объект {"value": "...", "isReadOnly":..., "isRequired":...}.
	raw := json.RawMessage(`{"displayName":{"value":"DisplayName_key","isReadOnly":false,"isRequired":true}}`)
	if got := resourceKey(raw, "displayName"); got != "DisplayName_key" {
		t.Errorf("resourceKey displayName = %q", got)
	}
	if got := resourceKey(raw, "missing"); got != "missing" {
		t.Errorf("resourceKey missing = %q, want запасной ключ", got)
	}
	// Простое строковое поле тоже работает (fallback).
	if got := resourceKey(json.RawMessage(`{"name":"plain"}`), "name"); got != "plain" {
		t.Errorf("resourceKey строкового = %q", got)
	}
}

func TestLocalizationItems(t *testing.T) {
	items := localizationItems(map[string]string{"ru-RU": "Поставщик", "": "Поставщик"})
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[""] != "Поставщик" || items["ru-RU"] != "Поставщик" {
		t.Errorf("items = %v", items)
	}
}

func TestMatchConfiguration(t *testing.T) {
	raw := json.RawMessage(`{"activeConfigurationName":"DEFAULT","configurations":[{"name":"DEFAULT"},{"name":"Other"}]}`)
	if got := matchConfiguration(raw, "default"); got != "DEFAULT" {
		t.Errorf("по имени = %q", got)
	}
	if got := matchConfiguration(raw, "other"); got != "Other" {
		t.Errorf("по имени 2 = %q", got)
	}
	if got := matchConfiguration(raw, "нет такой"); got != "" {
		t.Errorf("отсутствует = %q", got)
	}
}

func TestFirstBaseType(t *testing.T) {
	raw := json.RawMessage(`{"defaultValueId":"base-1","developmentItemTypes":[{"id":"base-1"},{"id":"base-2"}]}`)
	if got := firstBaseType(raw); got != "base-1" {
		t.Errorf("firstBaseType = %q", got)
	}
	if got := firstBaseType(json.RawMessage(`{"developmentItemTypes":[{"id":"base-2"}]}`)); got != "base-2" {
		t.Errorf("firstBaseType (без default) = %q", got)
	}
}

func TestDisplayNameKeys(t *testing.T) {
	raw := json.RawMessage(`{
		"displayName":{"value":"DisplayName_x","isReadOnly":false,"isRequired":true},
		"accusativeDisplayName":{"value":"AccusativeDisplayName_x","isReadOnly":false,"isRequired":true}
	}`)
	keys := displayNameKeys(raw)
	if keys["displayName"] != "DisplayName_x" {
		t.Errorf("displayName = %q", keys["displayName"])
	}
	if keys["accusativeDisplayName"] != "AccusativeDisplayName_x" {
		t.Errorf("accusativeDisplayName = %q", keys["accusativeDisplayName"])
	}
	if _, ok := keys["collectionDisplayName"]; ok {
		t.Error("collectionDisplayName не должен появляться, если его нет в метаданных")
	}
	// Пустое значение (ключ ресурса ещё не задан) пропускается.
	if got := displayNameKeys(json.RawMessage(`{"displayName":{"value":""}}`)); len(got) != 0 {
		t.Errorf("пустое значение должно пропускаться, получено %v", got)
	}
}
