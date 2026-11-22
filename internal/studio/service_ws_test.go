package studio

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// rpcCallRecord зафиксированный вызов фейка: метод и params в исходном виде.
type rpcCallRecord struct {
	method string
	params json.RawMessage
}

// recordingFake пишет все RPC-вызовы в лог и отвечает заглушками;
// для CreateMetadataElement возвращает itemId и ключ локализации DisplayName.
func recordingFake(t *testing.T) (*fakeStudio, func() []rpcCallRecord) {
	t.Helper()
	var mu sync.Mutex
	var calls []rpcCallRecord
	f := newFakeStudio(t, func(method string, params json.RawMessage) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, rpcCallRecord{method, params})
		if method == "MetadataEditors/DatabookMetadata/Property/CreateMetadataElement" {
			return map[string]any{"result": map[string]any{
				"itemId": "item-1",
				"editorMetadata": map[string]any{"properties": []map[string]any{
					{"id": "item-1", "displayName": map[string]any{"value": "Property_1_db-1"}},
				}},
			}}
		}
		return nil // остальным методам хватит пустого result
	})
	return f, func() []rpcCallRecord {
		mu.Lock()
		defer mu.Unlock()
		return append([]rpcCallRecord(nil), calls...)
	}
}

// elementParams собирает параметры ChangeMetadataElementParameter: innerPartType → значение.
func elementParams(t *testing.T, calls []rpcCallRecord) map[string]any {
	t.Helper()
	got := map[string]any{}
	for _, c := range calls {
		if !strings.HasSuffix(c.method, "Property/ChangeMetadataElementParameter") {
			continue
		}
		var args []any
		if err := json.Unmarshal(c.params, &args); err != nil {
			t.Fatalf("params %s: %v", c.params, err)
		}
		// (developmentId, metadataId, collectionName, metadataPaths, value)
		if len(args) != 5 {
			t.Fatalf("ChangeMetadataElementParameter: %d аргументов вместо 5: %s", len(args), c.params)
		}
		paths, ok := args[3].([]any)
		if !ok || len(paths) != 1 {
			t.Fatalf("metadataPaths не один шаг: %s", c.params)
		}
		step, ok := paths[0].(map[string]any)
		if !ok {
			t.Fatalf("шаг пути не объект: %s", c.params)
		}
		part, _ := step["metadataInnerPartType"].(string)
		got[part] = args[4]
	}
	return got
}

func TestAddPropertySequence(t *testing.T) {
	f, recorded := recordingFake(t)
	s := New(f.wsURL(), 5*time.Second, time.Minute)
	s.devID = "scope-1" // разработка «открыта»

	id, err := s.AddProperty(context.Background(), PropertyInput{
		MetadataID: "db-1", MetadataType: "Databook", Name: "Qty", Code: "QtyCode",
		PropertyType: "String", Length: 77, Required: true,
		DisplayName: map[string]string{"": "Кол-во"},
	})
	if err != nil {
		t.Fatalf("AddProperty: %v", err)
	}
	if id != "item-1" {
		t.Fatalf("itemId = %q, want item-1", id)
	}

	calls := recorded()
	var methods []string
	for _, c := range calls {
		methods = append(methods, c.method)
	}
	want := []string{
		"MetadataEditors/DatabookMetadata/Property/CreateMetadataElement",
		"MetadataEditors/ChangeLocalizationParameter",                          // отображаемое имя
		"MetadataEditors/DatabookMetadata/Property/ChangeMetadataElementParameter", // name
		"MetadataEditors/DatabookMetadata/Property/ChangeMetadataElementParameter", // code
		"MetadataEditors/DatabookMetadata/Property/ChangeMetadataElementParameter", // length
		"MetadataEditors/DatabookMetadata/Property/ChangeMetadataElementParameter", // isRequired
		"MetadataEditors/SaveMetadata",
	}
	if strings.Join(methods, "|") != strings.Join(want, "|") {
		t.Fatalf("последовательность вызовов:\n got %v\nwant %v", methods, want)
	}
	// developmentId подставлен первым позиционным аргументом, metadataId — вторым.
	var args []any
	if err := json.Unmarshal(calls[0].params, &args); err != nil {
		t.Fatalf("CreateMetadataElement params: %v", err)
	}
	if args[0] != "scope-1" || args[1] != "db-1" {
		t.Fatalf("CreateMetadataElement args[0:2] = %v, want [scope-1 db-1]", args[0:2])
	}
	// Параметры name/code/length/isRequired заданы со своими значениями.
	got := elementParams(t, calls)
	if got["name"] != "Qty" {
		t.Errorf("name = %v, want Qty", got["name"])
	}
	if got["code"] != "QtyCode" {
		t.Errorf("code = %v, want QtyCode (не имя свойства)", got["code"])
	}
	if got["length"] != float64(77) {
		t.Errorf("length = %v, want 77", got["length"])
	}
	if got["isRequired"] != true {
		t.Errorf("isRequired = %v, want true", got["isRequired"])
	}
}

func TestAddPropertyTypedParams(t *testing.T) {
	f, recorded := recordingFake(t)
	s := New(f.wsURL(), 5*time.Second, time.Minute)
	s.devID = "scope-1"

	if _, err := s.AddProperty(context.Background(), PropertyInput{
		MetadataID: "db-1", MetadataType: "Databook", Name: "When",
		PropertyType: "DateTime", DateTimeFormat: "Date",
	}); err != nil {
		t.Fatalf("AddProperty DateTime: %v", err)
	}
	if got := elementParams(t, recorded()); got["dateTimeFormat"] != "Date" {
		t.Fatalf("dateTimeFormat = %v, want Date", got["dateTimeFormat"])
	}

	if _, err := s.AddProperty(context.Background(), PropertyInput{
		MetadataID: "db-1", MetadataType: "Databook", Name: "Counterparty",
		PropertyType: "Navigation", EntityType: "guid-9",
	}); err != nil {
		t.Fatalf("AddProperty Navigation: %v", err)
	}
	got := elementParams(t, recorded())
	if got["entityGuid"] != "guid-9" {
		t.Fatalf("entityGuid = %v, want guid-9", got["entityGuid"])
	}
	// length у не-String не отправляется вовсе.
	if _, ok := got["length"]; ok {
		t.Errorf("length отправлен для Navigation: %v", got["length"])
	}
}

func TestNoDevelopmentGuard(t *testing.T) {
	f := newFakeStudio(t, nil)
	s := New(f.wsURL(), time.Second, time.Minute)
	for name, fn := range map[string]func() error{
		"AddProperty": func() error {
			_, err := s.AddProperty(context.Background(), PropertyInput{MetadataID: "db-1", MetadataType: "Databook", Name: "X"})
			return err
		},
		"GetMetadata": func() error {
			_, err := s.GetMetadata(context.Background(), "db-1", "Databook")
			return err
		},
		"CreateItem": func() error {
			_, err := s.CreateItem(context.Background(), "Databook", "mod-1", "X", "", false)
			return err
		},
	} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "разработка не открыта") {
			t.Errorf("%s без открытой разработки: %v", name, err)
		}
	}
}

func TestOpenDevelopmentKeepsStateOnLoadFailure(t *testing.T) {
	f := newFakeStudio(t, func(method string, _ json.RawMessage) map[string]any {
		switch method {
		case "DevelopmentLifetime/GetDevelopmentConfigurations":
			return map[string]any{"result": map[string]any{
				"activeConfigurationName": "DEFAULT",
				"configurations":          []map[string]any{{"name": "DEFAULT"}},
			}}
		case "DevelopmentLifetime/OpenDevelopment":
			return map[string]any{"result": "scope-9"}
		case "DevelopmentLifetime/LoadDevelopment":
			return map[string]any{"error": map[string]any{"code": -32000, "message": "не загрузилась"}}
		}
		return nil
	})
	s := New(f.wsURL(), time.Second, time.Minute)
	if _, err := s.OpenDevelopment(context.Background(), "DEFAULT"); err == nil {
		t.Fatal("ожидалась ошибка LoadDevelopment")
	}
	if id := s.DevelopmentID(); id != "" {
		t.Fatalf("devID = %q после неудачной загрузки, want пусто", id)
	}
	if name := s.DevelopmentName(); name != "" {
		t.Fatalf("devName = %q после неудачной загрузки, want пусто", name)
	}
}
