package catalog

import "testing"

const csdl = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx">
 <edmx:DataServices>
  <Schema Namespace="Sungero" xmlns="http://docs.oasis-open.org/odata/ns/edm">
   <EntityType Name="Entity"><Key><PropertyRef Name="Id"/></Key><Property Name="Id" Type="Edm.Int64" Nullable="false"/></EntityType>
   <EntityType Name="Contract" BaseType="Sungero.Entity">
     <Property Name="Name" Type="Edm.String"/><Property Name="TotalAmount" Type="Edm.Double"/>
     <NavigationProperty Name="Counterparty" Type="Sungero.Company"/>
     <NavigationProperty Name="Versions" Type="Collection(Sungero.Version)"/>
   </EntityType>
   <EntityType Name="Company" BaseType="Sungero.Entity"><Property Name="TIN" Type="Edm.String"/></EntityType>
   <EntityType Name="Version"><Property Name="Number" Type="Edm.Int32"/></EntityType>
  </Schema>
  <Schema Namespace="Sungero" xmlns="http://docs.oasis-open.org/odata/ns/edm">
   <EntityContainer Name="Container">
    <EntitySet Name="IContracts" EntityType="Sungero.Contract"/>
    <EntitySet Name="ICompanies" EntityType="Sungero.Company"/>
    <EntitySet Name="IContractCategories" EntityType="Sungero.Entity"/>
   </EntityContainer>
  </Schema>
 </edmx:DataServices>
</edmx:Edmx>`

// Метаданные решения eTMF: документ, зоны, секции, исследования, центры.
const etmfCSDL = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx">
 <edmx:DataServices>
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
   <EntityType Name="TrialSite" BaseType="Clinical.Entity"><Property Name="Name" Type="Edm.String"/></EntityType>
   <EntityContainer Name="Container">
    <EntitySet Name="IEtmfDocuments" EntityType="Clinical.EtmfDocument"/>
    <EntitySet Name="IEtmfZones" EntityType="Clinical.EtmfZone"/>
    <EntitySet Name="IEtmfSections" EntityType="Clinical.EtmfSection"/>
    <EntitySet Name="IStudies" EntityType="Clinical.Study"/>
    <EntitySet Name="ITrialSites" EntityType="Clinical.TrialSite"/>
   </EntityContainer>
  </Schema>
 </edmx:DataServices>
</edmx:Edmx>`

func TestFindEtmfVocabulary(t *testing.T) {
	c, err := Parse([]byte(etmfCSDL))
	if err != nil {
		t.Fatal(err)
	}
	// Какой набор должен быть первым в выдаче.
	for q, want := range map[string]string{
		"зоны":           "IEtmfZones",
		"секции":         "IEtmfSections",
		"исследования":   "IStudies",
		"испытания":      "IStudies",
		"центры":         "ITrialSites",
		"документы":      "IEtmfDocuments",
		"документы etmf": "IEtmfDocuments",
		"зоны etmf":      "IEtmfZones",
		"eTMF Documents": "IEtmfDocuments",
	} {
		got := c.Find(q, 5)
		if len(got) == 0 || got[0].Name != want {
			t.Errorf("поиск %q: первый должен быть %s, получено %+v", q, want, got)
		}
	}
	// Голое «etmf» ловит все наборы решения; IEtmfDocuments среди них, пусть и не первым
	// (при равном счёте выигрывает короткое имя IEtmfZones).
	found := false
	for _, s := range c.Find("etmf", 5) {
		if s.Name == "IEtmfDocuments" {
			found = true
		}
	}
	if !found {
		t.Error("поиск «etmf» не нашёл IEtmfDocuments")
	}
}

func TestParseFindDescribe(t *testing.T) {
	c, err := Parse([]byte(csdl))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Sets) != 3 {
		t.Fatalf("наборов %d", len(c.Sets))
	}
	if got := c.Find("договоры", 5); len(got) != 2 || got[0].Name != "IContracts" {
		t.Errorf("поиск по-русски: %+v", got)
	}
	if got := c.Find("compan", 5); len(got) != 1 || got[0].Name != "ICompanies" {
		t.Errorf("поиск по-английски: %+v", got)
	}
	if got := c.Find("контрагент", 5); len(got) != 1 || got[0].Name != "ICompanies" {
		t.Errorf("контрагент: %+v", got)
	}
	s, props, navs, ok := c.Describe("icontracts")
	if !ok || s.Name != "IContracts" || len(props) != 3 || props[0].Name != "Id" || len(navs) != 2 {
		t.Fatalf("describe: %v %+v %+v", ok, props, navs)
	}
	if navs[0].Name != "Counterparty" || navs[0].Type != "Company" || navs[1].Type != "Version[]" {
		t.Errorf("ссылки: %+v", navs)
	}
	if _, _, _, ok := c.Describe("Nope"); ok {
		t.Error("несуществующий набор")
	}
	if _, err := Parse([]byte("<x/>")); err == nil {
		t.Error("пустые метаданные должны давать ошибку")
	}
}
