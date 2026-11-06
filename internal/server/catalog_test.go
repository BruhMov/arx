package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

const testCSDL = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx"><edmx:DataServices>
<Schema Namespace="Sungero" xmlns="http://docs.oasis-open.org/odata/ns/edm">
 <EntityType Name="Contract"><Property Name="Id" Type="Edm.Int64"/><Property Name="Name" Type="Edm.String"/>
  <NavigationProperty Name="Counterparty" Type="Sungero.Company"/></EntityType>
 <EntityType Name="Company"><Property Name="Id" Type="Edm.Int64"/><Property Name="TIN" Type="Edm.String"/></EntityType>
 <EntityContainer Name="C"><EntitySet Name="IContracts" EntityType="Sungero.Contract"/><EntitySet Name="ICompanies" EntityType="Sungero.Company"/></EntityContainer>
</Schema></edmx:DataServices></edmx:Edmx>`

// поддельный сервис интеграции: метаданные, список договоров, один договор, одно действие
func fakeOData(t *testing.T, calls *[]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/odata/$metadata":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, testCSDL)
		case r.URL.Path == "/odata/IContracts":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"@odata.context":"x","@odata.count":2,"value":[{"Id":7,"Name":"Поставка"},{"Id":9,"Name":"Аренда"}]}`)
		case r.URL.Path == "/odata/IContracts(7)":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"@odata.context":"x", "Id":7, "Name":"Поставка"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/odata/Docflow/StartTask":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connectCatalog(t *testing.T, base string, write bool) *mcp.ClientSession {
	t.Helper()
	cl, err := odata.New(odata.Options{BaseURL: base + "/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	svc := rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC})
	s := server.New(svc, server.Options{Version: "test", AllowWrite: write, NoConfirm: true, MaxTextChars: 20000, OData: cl})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestCatalogTools(t *testing.T) {
	var calls []string
	srv := fakeOData(t, &calls)
	cs := connectCatalog(t, srv.URL, false)
	tools := toolNames(t, cs)
	for _, n := range []string{"rx_find_entity", "rx_describe_entity", "rx_query"} {
		if tools[n] == nil || !tools[n].Annotations.ReadOnlyHint {
			t.Fatalf("нет %s или он не помечен как чтение", n)
		}
	}
	if tools["rx_call_action"] != nil {
		t.Fatal("rx_call_action не должен быть виден без разрешения записи")
	}
	if out := callText(t, cs, "rx_find_entity", map[string]any{"query": "договоры"}); !strings.Contains(out, "IContracts: полей 2, ссылок 1") {
		t.Errorf("find: %s", out)
	}
	if out := callText(t, cs, "rx_describe_entity", map[string]any{"entity": "IContracts"}); !strings.Contains(out, "Name: String") || !strings.Contains(out, "Counterparty → Company") {
		t.Errorf("describe: %s", out)
	}
	out := callText(t, cs, "rx_query", map[string]any{"entity": "IContracts", "filter": "contains(Name,'а')", "select": "Id,Name", "top": 500})
	if !strings.Contains(out, "показано 2, всего по условию 2") || !strings.Contains(out, `{"Id":7,"Name":"Поставка"}`) {
		t.Errorf("query list: %s", out)
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last, "%24top=100") || !strings.Contains(last, "%24count=true") || !strings.Contains(last, "%24select=Id%2CName") {
		t.Errorf("запрос к RX: %s", last)
	}
	if out := callText(t, cs, "rx_query", map[string]any{"entity": "IContracts", "id": 7}); !strings.Contains(out, `IContracts #7`) || strings.Contains(out, "odata.context") {
		t.Errorf("query one: %s", out)
	}
	if out := callText(t, cs, "rx_query", map[string]any{"entity": "IContracts(1)/../x"}); !strings.Contains(out, "имя набора") {
		t.Errorf("подозрительное имя набора должно отклоняться: %s", out)
	}
	// метаданные читаются один раз, а не на каждый вызов
	n := 0
	for _, c := range calls {
		if strings.Contains(c, "$metadata") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("$metadata запрошен %d раз", n)
	}
}

func TestCallActionOnlyWithWrite(t *testing.T) {
	var calls []string
	srv := fakeOData(t, &calls)
	cs := connectCatalog(t, srv.URL, true)
	if toolNames(t, cs)["rx_call_action"] == nil {
		t.Fatal("при разрешённой записи rx_call_action должен быть")
	}
	out := callText(t, cs, "rx_call_action", map[string]any{"action": "Docflow/StartTask", "params": map[string]any{"taskId": 5}})
	if !strings.Contains(out, "Docflow/StartTask выполнено") {
		t.Errorf("call: %s", out)
	}
	if bad := callText(t, cs, "rx_call_action", map[string]any{"action": "../x"}); !strings.Contains(bad, "формат") {
		t.Errorf("кривое имя действия: %s", bad)
	}
}

// Полный цикл eTMF: найти сущности решения → увидеть поля документа → прочитать
// справочник зон → создать eTMF-документ с Id зоны из справочника.
func TestCatalogEtmfWorkflow(t *testing.T) {
	const etmfMeta = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx"><edmx:DataServices>
<Schema Namespace="Clinical" xmlns="http://docs.oasis-open.org/odata/ns/edm">
 <EntityType Name="Entity"><Key><PropertyRef Name="Id"/></Key><Property Name="Id" Type="Edm.Int64" Nullable="false"/></EntityType>
 <EntityType Name="EtmfDocument" BaseType="Clinical.Entity">
  <Property Name="Name" Type="Edm.String"/><Property Name="Version" Type="Edm.String"/>
  <Property Name="ICHCode" Type="Edm.String"/><Property Name="EffectiveDate" Type="Edm.DateTimeOffset"/>
  <NavigationProperty Name="Zone" Type="Clinical.EtmfZone"/>
  <NavigationProperty Name="Section" Type="Clinical.EtmfSection"/>
  <NavigationProperty Name="Study" Type="Clinical.Study"/>
 </EntityType>
 <EntityType Name="EtmfZone" BaseType="Clinical.Entity"><Property Name="Name" Type="Edm.String"/><Property Name="Code" Type="Edm.String"/></EntityType>
 <EntityType Name="EtmfSection" BaseType="Clinical.Entity"><Property Name="Name" Type="Edm.String"/></EntityType>
 <EntityType Name="Study" BaseType="Clinical.Entity"><Property Name="Name" Type="Edm.String"/></EntityType>
 <EntityContainer Name="C">
  <EntitySet Name="IEtmfDocuments" EntityType="Clinical.EtmfDocument"/>
  <EntitySet Name="IEtmfZones" EntityType="Clinical.EtmfZone"/>
  <EntitySet Name="IEtmfSections" EntityType="Clinical.EtmfSection"/>
  <EntitySet Name="IStudies" EntityType="Clinical.Study"/>
 </EntityContainer>
</Schema></edmx:DataServices></edmx:Edmx>`
	var created []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/odata/$metadata":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, etmfMeta)
		case r.URL.Path == "/odata/IEtmfZones":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"@odata.count":1,"value":[{"Id":3,"Name":"01. Управление исследованием (Trial Management)","Code":"01"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/odata/IEtmfDocuments":
			var body map[string]any
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &body)
			created = append(created, body)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"Id":2001,"Name":"План управления исследованием"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cs := connectCatalog(t, srv.URL, true)

	// 1. Найти наборы решения eTMF русскими словами.
	if out := callText(t, cs, "rx_find_entity", map[string]any{"query": "зоны etmf"}); !strings.Contains(out, "IEtmfZones") {
		t.Errorf("find: %s", out)
	}
	if out := callText(t, cs, "rx_find_entity", map[string]any{"query": "исследования"}); !strings.Contains(out, "IStudies") {
		t.Errorf("find исследования: %s", out)
	}
	// 2. Поля и ссылки документа — то, что уходит в rx_create_etmf_document.
	if out := callText(t, cs, "rx_describe_entity", map[string]any{"entity": "IEtmfDocuments"}); !strings.Contains(out, "EffectiveDate: DateTimeOffset") || !strings.Contains(out, "Zone → EtmfZone") || !strings.Contains(out, "Name: String") {
		t.Errorf("describe: %s", out)
	}
	// 3. Прочитать справочник зон и взять Id.
	if out := callText(t, cs, "rx_query", map[string]any{"entity": "IEtmfZones", "select": "Id,Name", "filter": "contains(Name,'Управление')"}); !strings.Contains(out, `"Id":3`) || !strings.Contains(out, "Trial Management") {
		t.Errorf("query зоны: %s", out)
	}
	// 4. Создать документ с найденными Id.
	out := callText(t, cs, "rx_create_etmf_document", map[string]any{
		"name": "План управления исследованием (Trial Management Plan)", "zone_id": 3, "study_id": 102,
		"version": "1", "effective_date": "2026-08-23", "ich_code": "5.6", "sponsor_document": true,
	})
	if !strings.Contains(out, "#2001") {
		t.Errorf("создание eTMF-документа: %s", out)
	}
	if len(created) != 1 {
		t.Fatalf("запросов создания: %d", len(created))
	}
	c := created[0]
	if z, _ := c["Zone"].(map[string]any); z["Id"] != float64(3) {
		t.Errorf("зона из справочника не попала в документ: %v", c)
	}
	if s, _ := c["Study"].(map[string]any); s["Id"] != float64(102) {
		t.Errorf("исследование не попало в документ: %v", c)
	}
	if c["SponsorDocument"] != true || c["ICHCode"] != "5.6" || c["EffectiveDate"] != "2026-08-23T00:00:00Z" {
		t.Errorf("простые поля: %v", c)
	}
}
