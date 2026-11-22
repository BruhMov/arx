package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// errNoDevelopment возвращают методы, которым нужна открытая разработка.
var errNoDevelopment = errors.New("разработка не открыта: сначала dev_open_development")

// Service высокоуровневые операции над Development Studio. Держит состояние открытой
// разработки (developmentId) и прячет многошаговые RPC-последовательности.
//
// Формы параметров взяты из рабочей конфигурации Studio 26.2.0.0066 (внутренний
// интерфейс); на других версиях требуют проверки. Точные имена полей options, где они
// не были зафиксированы, помечены как «best-effort»: при расхождении используйте dev_call.
type Service struct {
	url      string
	timeout  time.Duration // обычный RPC-вызов
	deployTO time.Duration // публикация идёт минуты

	mu      sync.Mutex
	c       *Client
	devID   string
	devName string
}

// New создаёт сервис, не подключаясь к Studio: соединение открывается лениво при первом
// вызове, чтобы сервер работал и когда Studio не запущена.
func New(url string, timeout, deployTimeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if deployTimeout <= 0 {
		deployTimeout = 10 * time.Minute
	}
	return &Service{url: url, timeout: timeout, deployTO: deployTimeout}
}

// DevelopmentID открытая разработка (GUID), пусто, если ещё не открыта.
func (s *Service) DevelopmentID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devID
}

// DevelopmentName имя открытой разработки, пусто, если ещё не открыта.
func (s *Service) DevelopmentName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devName
}

// call выполняет RPC-метод редактора/сборщика: параметры идут в поле options, а
// developmentId открытой разработки подставляется отдельным параметром на верхнем уровне.
func (s *Service) call(ctx context.Context, method string, options map[string]any) (json.RawMessage, error) {
	devID := s.DevelopmentID()
	if devID == "" {
		return nil, errNoDevelopment
	}
	params := map[string]any{"options": options, "developmentId": devID}
	return s.callRaw(ctx, method, params)
}

// callParams выполняет RPC-метод с произвольными params (методы без options).
func (s *Service) callParams(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return s.callRaw(ctx, method, params)
}

// callArray выполняет RPC-метод с позиционными параметрами (массивом).
// Первым элементом автоматически подставляется developmentId.
func (s *Service) callArray(ctx context.Context, method string, args []any) (json.RawMessage, error) {
	devID := s.DevelopmentID()
	if devID == "" {
		return nil, errNoDevelopment
	}
	full := make([]any, 0, len(args)+1)
	full = append(full, devID)
	full = append(full, args...)
	return s.callRaw(ctx, method, full)
}

// CallRaw выполняет произвольный метод Studio с параметрами на верхнем уровне (для dev_call).
// К параметрам автоматически добавляется developmentId открытой разработки, если не задан.
func (s *Service) CallRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return s.callRaw(ctx, method, s.opts(params))
}

func (s *Service) callRaw(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c == nil {
		c, err := Dial(ctx, s.url, s.timeout)
		if err != nil {
			return nil, err
		}
		s.c = c
	}
	res, err := s.c.CallParams(ctx, method, params)
	if err != nil {
		if IsRPCError(err) {
			return nil, err // ошибка Studio, соединение живо
		}
		// Транспортный сбой: соединение под вопросом, переподключимся в следующий раз.
		_ = s.c.Close()
		s.c = nil
		return nil, err
	}
	return res, nil
}

// opts добавляет developmentId открытой разработки к параметрам вызова.
func (s *Service) opts(extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra)+1)
	for k, v := range extra {
		out[k] = v
	}
	if devID := s.DevelopmentID(); devID != "" {
		if _, ok := out["developmentId"]; !ok {
			out["developmentId"] = devID
		}
	}
	return out
}

// --- подключение и дерево ---

// About данные о Studio (About/GetAboutData), необработанный JSON.
func (s *Service) About(ctx context.Context) (json.RawMessage, error) {
	return s.callParams(ctx, "About/GetAboutData", nil)
}

// Configurations список конфигураций разработки.
func (s *Service) Configurations(ctx context.Context) (json.RawMessage, error) {
	return s.callParams(ctx, "DevelopmentLifetime/GetDevelopmentConfigurations", nil)
}

// OpenDevelopment открывает и загружает разработку по имени конфигурации.
// OpenDevelopment(configurationName) возвращает scope-строку, которая и есть developmentId
// для последующих вызовов; LoadDevelopment загружает её, GetDevelopmentTree отдаёт дерево.
func (s *Service) OpenDevelopment(ctx context.Context, name string) (json.RawMessage, error) {
	cfgs, err := s.Configurations(ctx)
	if err != nil {
		return nil, err
	}
	cfgName := matchConfiguration(cfgs, name)
	if cfgName == "" {
		return nil, fmt.Errorf("разработка %q не найдена в списке конфигураций Studio", name)
	}
	raw, err := s.callParams(ctx, "DevelopmentLifetime/OpenDevelopment", map[string]any{"configurationName": cfgName})
	if err != nil {
		return nil, err
	}
	scope, err := plainString(raw)
	if err != nil {
		return nil, fmt.Errorf("OpenDevelopment: %w", err)
	}
	// Разработку считаем открытой только после успешной загрузки: при сбое LoadDevelopment
	// прежнее состояние (в том числе «ничего не открыто») сохраняется.
	if _, err := s.callParams(ctx, "DevelopmentLifetime/LoadDevelopment", map[string]any{"developmentId": scope}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.devID = scope
	s.devName = cfgName
	s.mu.Unlock()
	return s.Tree(ctx)
}

// Tree дерево объектов разработки.
func (s *Service) Tree(ctx context.Context) (json.RawMessage, error) {
	devID := s.DevelopmentID()
	if devID == "" {
		return nil, errNoDevelopment
	}
	return s.callParams(ctx, "DevelopmentExplorer/GetDevelopmentTree", map[string]any{"developmentId": devID})
}

// FilterTree поиск по дереву разработки.
func (s *Service) FilterTree(ctx context.Context, query string) (json.RawMessage, error) {
	return s.callParams(ctx, "DevelopmentExplorer/FilterDevelopmentTree", map[string]any{"developmentId": s.DevelopmentID(), "query": query})
}

// matchConfiguration находит имя конфигурации по имени (или его части) в ответе
// GetDevelopmentConfigurations: {"activeConfigurationName":..., "configurations":[{"name":...}]}.
func matchConfiguration(raw json.RawMessage, name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return "" // пустой запрос не должен молча матчить первую конфигурацию
	}
	var m struct {
		ActiveConfigurationName string `json:"activeConfigurationName"`
		Configurations          []struct {
			Name string `json:"name"`
		} `json:"configurations"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, c := range m.Configurations {
		if strings.EqualFold(c.Name, lower) || strings.Contains(strings.ToLower(c.Name), lower) {
			return c.Name
		}
	}
	if strings.EqualFold(m.ActiveConfigurationName, lower) || strings.Contains(strings.ToLower(m.ActiveConfigurationName), lower) {
		return m.ActiveConfigurationName
	}
	return ""
}

// --- чтение и сохранение метаданных ---

// GetMetadata метаданные объекта (MetadataEditors/GetMetadata).
func (s *Service) GetMetadata(ctx context.Context, metadataID, metadataType string) (json.RawMessage, error) {
	return s.call(ctx, "MetadataEditors/GetMetadata", map[string]any{"metadataId": metadataID, "metadataType": metadataType})
}

// SaveMetadata сохраняет метаданные объекта (MetadataEditors/SaveMetadata).
func (s *Service) SaveMetadata(ctx context.Context, metadataID, metadataType string) error {
	_, err := s.call(ctx, "MetadataEditors/SaveMetadata", map[string]any{"metadataId": metadataID, "metadataType": metadataType})
	return err
}

// SetParameter изменяет параметр объекта (MetadataEditors/ChangeMetadataParameter).
func (s *Service) SetParameter(ctx context.Context, metadataID, metadataType, name string, value any) error {
	_, err := s.call(ctx, "MetadataEditors/ChangeMetadataParameter", map[string]any{
		"metadataId":   metadataID,
		"metadataType": metadataType,
		"parameter":    map[string]any{"name": name, "value": value},
	})
	return err
}

// SetLocalization задаёт локализованное название (MetadataEditors/ChangeLocalizationParameter).
// values — отображение «локаль → текст»; пустая локаль "" это нейтральная, «ru»/«ru-RU» — русские.
func (s *Service) SetLocalization(ctx context.Context, metadataID, metadataType, key string, values map[string]string) error {
	_, err := s.call(ctx, "MetadataEditors/ChangeLocalizationParameter", map[string]any{
		"metadataId":   metadataID,
		"metadataType": metadataType,
		"parameter": map[string]any{
			"name":  key,
			"value": map[string]any{"items": localizationItems(values)},
		},
	})
	return err
}

// localizationItems упаковывает «локаль → текст» в словарь items (Studio ждёт объект,
// а не массив): {"items": {"": "...", "ru": "...", "ru-RU": "..."}}.
func localizationItems(values map[string]string) map[string]string {
	return values
}

// resourceKey достаёт из метаданных ключ ресурса локализации по имени поля (displayName,
// accusativeDisplayName и т.п.). Поля метаданных имеют вид {"value":"...","isReadOnly":...}.
func resourceKey(raw json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if v, ok := m[field]; ok {
			var obj struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(v, &obj) == nil && obj.Value != "" {
				return obj.Value
			}
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
		}
	}
	return field
}

// --- создание объектов разработки ---

// CreateItem создаёт объект разработки (DevelopmentBuilders/CreateDevelopmentItem).
// parentID/baseID пустые означают null. Возвращает GUID созданного объекта.
func (s *Service) CreateItem(ctx context.Context, metadataType, parentID, name, baseID string, isAbstract bool) (string, error) {
	raw, err := s.call(ctx, "DevelopmentBuilders/CreateDevelopmentItem", map[string]any{
		"metadataType":          metadataType,
		"name":                  name,
		"parentId":              orNull(parentID),
		"baseDevelopmentItemId": orNull(baseID),
		"isAbstract":            isAbstract,
	})
	if err != nil {
		return "", err
	}
	return guidFromResult(raw)
}

// GetBaseTypes допустимые базовые типы (DevelopmentBuilders/GetDevelopmentItemBaseTypes).
func (s *Service) GetBaseTypes(ctx context.Context, parentID, metadataType string) (json.RawMessage, error) {
	return s.call(ctx, "DevelopmentBuilders/GetDevelopmentItemBaseTypes", map[string]any{"parentId": parentID, "metadataType": metadataType})
}

// DeleteItem удаляет объект разработки (DevelopmentBuilders/DeleteDevelopmentItem).
func (s *Service) DeleteItem(ctx context.Context, id, metadataType string) error {
	_, err := s.call(ctx, "DevelopmentBuilders/DeleteDevelopmentItem", map[string]any{"id": id, "metadataType": metadataType})
	return err
}

// CreateSolution создаёт решение и сохраняет его. Возвращает GUID решения.
func (s *Service) CreateSolution(ctx context.Context, name string) (string, error) {
	id, err := s.CreateItem(ctx, "Solution", "", name, "", false)
	if err != nil {
		return id, err
	}
	return id, s.SaveMetadata(ctx, id, "Solution")
}

// CreateModule создаёт модуль в решении: тех. имя, отображаемое название по локалям,
// видимость, сохранение. Возвращает GUID модуля.
func (s *Service) CreateModule(ctx context.Context, parentID, name string, displayName map[string]string, isVisible *bool) (string, error) {
	id, err := s.CreateItem(ctx, "Module", parentID, name, "", false)
	if err != nil {
		return id, err
	}
	if len(displayName) > 0 {
		meta, err := s.GetMetadata(ctx, id, "Module")
		if err != nil {
			return id, err
		}
		if err := s.SetLocalization(ctx, id, "Module", resourceKey(meta, "displayName"), displayName); err != nil {
			return id, err
		}
	}
	if isVisible != nil {
		if err := s.SetParameter(ctx, id, "Module", "IsVisible", *isVisible); err != nil {
			return id, err
		}
	}
	return id, s.SaveMetadata(ctx, id, "Module")
}

// createNamedEntity создаёт объект заданного типа (Databook, Document, Task, Assignment,
// Notice, ReviewAssignment): базовый тип по умолчанию, локализованные названия из names
// (только те, что есть в метаданных этого типа), сохранение. Родитель — модуль для
// справочника/документа/задачи и сам тип задачи для задания/уведомления/задания на
// ознакомление.
func (s *Service) createNamedEntity(ctx context.Context, parentID, name, metadataType string, names map[string]map[string]string) (string, error) {
	baseID := ""
	if bt, err := s.GetBaseTypes(ctx, parentID, metadataType); err == nil {
		baseID = firstBaseType(bt)
	}
	id, err := s.CreateItem(ctx, metadataType, parentID, name, baseID, false)
	if err != nil {
		return id, err
	}
	meta, err := s.GetMetadata(ctx, id, metadataType)
	if err != nil {
		return id, err
	}
	keys := displayNameKeys(meta)
	for _, pair := range []struct{ key, metaField string }{
		{"DisplayName", "displayName"},
		{"AccusativeDisplayName", "accusativeDisplayName"},
		{"CollectionDisplayName", "collectionDisplayName"},
	} {
		vals, ok := names[pair.key]
		if !ok || len(vals) == 0 {
			continue
		}
		resKey, exists := keys[pair.metaField]
		if !exists {
			continue // у этого типа нет такого названия
		}
		if err := s.SetLocalization(ctx, id, metadataType, resKey, vals); err != nil {
			return id, err
		}
	}
	return id, s.SaveMetadata(ctx, id, metadataType)
}

// CreateDatabook создаёт справочник: базовый тип, локализованные названия и сохранение.
func (s *Service) CreateDatabook(ctx context.Context, parentID, name string, names map[string]map[string]string) (string, error) {
	return s.createNamedEntity(ctx, parentID, name, "Databook", names)
}

// CreateDocument создаёт тип документа.
func (s *Service) CreateDocument(ctx context.Context, parentID, name string, names map[string]map[string]string) (string, error) {
	return s.createNamedEntity(ctx, parentID, name, "Document", names)
}

// CreateTask создаёт тип задачи. Тип задачи может быть родителем заданий, уведомлений и
// заданий на ознакомление: они создаются с parent_id = GUID этого типа задачи.
func (s *Service) CreateTask(ctx context.Context, parentID, name string, names map[string]map[string]string) (string, error) {
	return s.createNamedEntity(ctx, parentID, name, "Task", names)
}

// CreateAssignment создаёт задание — подвид задачи. Как и уведомление с заданием на
// ознакомление, хранится в таблице заданий, но в Studio это отдельный тип.
func (s *Service) CreateAssignment(ctx context.Context, parentID, name string, names map[string]map[string]string) (string, error) {
	return s.createNamedEntity(ctx, parentID, name, "Assignment", names)
}

// CreateNotice создаёт уведомление — подвид задачи.
func (s *Service) CreateNotice(ctx context.Context, parentID, name string, names map[string]map[string]string) (string, error) {
	return s.createNamedEntity(ctx, parentID, name, "Notice", names)
}

// CreateReviewAssignment создаёт задание на ознакомление/рассмотрение — подвид задачи.
func (s *Service) CreateReviewAssignment(ctx context.Context, parentID, name string, names map[string]map[string]string) (string, error) {
	return s.createNamedEntity(ctx, parentID, name, "ReviewAssignment", names)
}

// displayNameKeys достаёт из метаданных ключи ресурсов для тех полей названия
// (displayName, accusativeDisplayName, collectionDisplayName), которые есть у этого типа.
func displayNameKeys(meta json.RawMessage) map[string]string {
	out := map[string]string{}
	var m map[string]json.RawMessage
	if json.Unmarshal(meta, &m) != nil {
		return out
	}
	for _, field := range []string{"displayName", "accusativeDisplayName", "collectionDisplayName"} {
		raw, ok := m[field]
		if !ok {
			continue
		}
		var v struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Value == "" {
			continue
		}
		out[field] = v.Value
	}
	return out
}

// --- свойства ---

// PropertyInput параметры нового свойства.
type PropertyInput struct {
	MetadataID     string            // GUID владельца (справочник/документ/задача/задание)
	MetadataType   string            // Databook | Document | Task | Assignment | Notice | ReviewAssignment
	Name           string            // техническое имя поля
	Code           string            // внутренний код свойства; по умолчанию = Name
	PropertyType   string            // String | Integer | Double | Boolean | DateTime | Navigation | Enumeration | Collection
	AddingItemKind string            // Property (по умолчанию) | CollectionProperty | CollectionChildProperty
	ParentID       string            // для поля коллекции: itemId коллекции
	MetadataPaths  []any             // путь к коллекции (для поля коллекции)
	DisplayName    map[string]string // локализованное название
	Length         int               // для String
	Required       bool
	DateTimeFormat string           // для DateTime, например Date
	EntityType     string           // для Navigation: допустимый тип
	EnumValues     []EnumValueInput // для Enumeration
}

// EnumValueInput вариант перечисления.
type EnumValueInput struct {
	Name        string
	DisplayName map[string]string
}

// AddProperty создаёт свойство и настраивает его по описанной последовательности:
// CreateMetadataElement → локализация DisplayName → ChangeMetadataElementParameter
// (name, code, propertyType, length, isRequired, dateTimeFormat, entityGuid)
// → (варианты перечисления) → SaveMetadata. Возвращает itemId нового свойства.
func (s *Service) AddProperty(ctx context.Context, in PropertyInput) (string, error) {
	adding := in.AddingItemKind
	if adding == "" {
		adding = "Property"
	}
	paths := []any{}
	if in.MetadataPaths != nil {
		paths = in.MetadataPaths
	}
	// CreateMetadataElement(developmentId, metadataId, collectionName, metadataPaths, name, addingItemKind, parentId)
	raw, err := s.callArray(ctx, editorMethod(in.MetadataType, "Property", "CreateMetadataElement"),
		[]any{in.MetadataID, "Properties", paths, in.Name, adding, orNull(in.ParentID)})
	if err != nil {
		return "", err
	}
	itemID, err := idFromResult(raw)
	if err != nil {
		return "", err
	}
	// Дальнейшие шаги правят уже созданный элемент: при сбое упоминаем itemId,
	// чтобы полунастроенное свойство можно было найти и удалить (dev_delete_property).
	setParam := func(param string, value any) error {
		if err := s.ChangeElementParameter(ctx, in.MetadataType, in.MetadataID, "Property", itemID, param, value); err != nil {
			return fmt.Errorf("свойство создано (itemId %s), но параметр %s не задан: %w", itemID, param, err)
		}
		return nil
	}
	// Отображаемое имя обязательно: задаём его по ключу локализации нового свойства.
	displayName := in.DisplayName
	if len(displayName) == 0 {
		displayName = map[string]string{"": in.Name}
	}
	if key := propertyDisplayNameKey(raw, itemID); key != "" {
		if err := s.SetLocalization(ctx, in.MetadataID, in.MetadataType, key, displayName); err != nil {
			return itemID, fmt.Errorf("свойство создано (itemId %s), но отображаемое имя не задано: %w", itemID, err)
		}
	}
	// Техническое имя и код свойства (по умолчанию автосгенерированы «Property1» и т.п.).
	if err := setParam("name", in.Name); err != nil {
		return itemID, err
	}
	code := in.Code
	if code == "" {
		code = in.Name
	}
	if err := setParam("code", code); err != nil {
		return itemID, err
	}
	// Тип данных свойства (String — по умолчанию, поэтому не задаём) и зависящие от него параметры.
	pt := in.PropertyType
	if pt != "" && !strings.EqualFold(pt, "String") {
		if err := setParam("propertyType", pt); err != nil {
			return itemID, err
		}
	}
	if in.Length > 0 && (pt == "" || strings.EqualFold(pt, "String")) {
		if err := setParam("length", in.Length); err != nil {
			return itemID, err
		}
	}
	if in.DateTimeFormat != "" && strings.EqualFold(pt, "DateTime") {
		if err := setParam("dateTimeFormat", in.DateTimeFormat); err != nil {
			return itemID, err
		}
	}
	if in.EntityType != "" && strings.EqualFold(pt, "Navigation") {
		if err := setParam("entityGuid", in.EntityType); err != nil {
			return itemID, err
		}
	}
	if in.Required {
		if err := setParam("isRequired", true); err != nil {
			return itemID, err
		}
	}
	// Перечисление — варианты значений.
	if strings.EqualFold(pt, "Enumeration") {
		for _, ev := range in.EnumValues {
			if err := s.addEnumValue(ctx, in.MetadataType, in.MetadataID, itemID, in.Name, ev); err != nil {
				return itemID, fmt.Errorf("свойство создано (itemId %s), но вариант перечисления %q не добавлен: %w", itemID, ev.Name, err)
			}
		}
	}
	return itemID, s.SaveMetadata(ctx, in.MetadataID, in.MetadataType)
}

// propertyDisplayNameKey достаёт ключ ресурса «Отображаемое имя» нового свойства из
// результата CreateMetadataElement: editorMetadata.properties[].displayName.value для id == itemId.
func propertyDisplayNameKey(raw json.RawMessage, itemID string) string {
	var res struct {
		EditorMetadata struct {
			Properties []struct {
				ID          string `json:"id"`
				DisplayName struct {
					Value string `json:"value"`
				} `json:"displayName"`
			} `json:"properties"`
		} `json:"editorMetadata"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return ""
	}
	for _, p := range res.EditorMetadata.Properties {
		if p.ID == itemID && p.DisplayName.Value != "" {
			return p.DisplayName.Value
		}
	}
	return ""
}

func (s *Service) addEnumValue(ctx context.Context, metadataType, metadataID, propertyID, propertyName string, ev EnumValueInput) error {
	// Создаём вариант перечисления в коллекции directValues свойства.
	raw, err := s.callArray(ctx, editorMethod(metadataType, "EnumValue", "CreateMetadataElement"),
		[]any{metadataID, "Properties", []any{map[string]any{"itemId": propertyID, "metadataInnerPartType": "directValues"}}, "", "EnumValue", nil})
	if err != nil {
		return err
	}
	itemID, err := idFromResult(raw)
	if err != nil {
		return err
	}
	// Задаём имя варианта: путь свойство → directValues → вариант → name.
	paths := []any{
		map[string]any{"itemId": propertyID, "metadataInnerPartType": "directValues"},
		map[string]any{"itemId": itemID, "metadataInnerPartType": "name"},
	}
	if _, err := s.callArray(ctx, editorMethod(metadataType, "EnumValue", "ChangeMetadataElementParameter"),
		[]any{metadataID, "Properties", paths, ev.Name}); err != nil {
		return err
	}
	// Отображаемое имя варианта (обязательно).
	displayName := ev.DisplayName
	if len(displayName) == 0 {
		displayName = map[string]string{"": ev.Name}
	}
	key := "Enum_" + propertyName + "_" + ev.Name + "_" + metadataID
	if err := s.SetLocalization(ctx, metadataID, metadataType, key, displayName); err != nil {
		return err
	}
	return nil
}

// ChangeElementParameter меняет параметр элемента коллекции метаданных.
// collection — сегмент в единственном числе: Property или EnumValue.
func (s *Service) ChangeElementParameter(ctx context.Context, metadataType, metadataID, collection string, itemID string, paramName string, value any) error {
	// ChangeMetadataElementParameter(developmentId, metadataId, metadataPartType, metadataPaths, value) — позиционно.
	// metadataPartType всегда "Properties" (коллекция верхнего уровня); параметр задаётся в metadataInnerPartType.
	paths := []any{map[string]any{"itemId": itemID, "metadataInnerPartType": paramName}}
	_, err := s.callArray(ctx, editorMethod(metadataType, collection, "ChangeMetadataElementParameter"),
		[]any{metadataID, "Properties", paths, value})
	return err
}

// DeleteProperty удаляет свойство (MetadataEditors/{Kind}Metadata/Property/DeleteMetadataElement).
// Сигнатура позиционная: (developmentId, metadataId, collectionName, metadataPaths, itemId).
func (s *Service) DeleteProperty(ctx context.Context, metadataType, metadataID string, itemID string) error {
	_, err := s.callArray(ctx, editorMethod(metadataType, "Property", "DeleteMetadataElement"),
		[]any{metadataID, "Properties", []any{}, itemID})
	return err
}

// GetNavigationEntityTypes допустимые типы для существующего ссылочного свойства.
func (s *Service) GetNavigationEntityTypes(ctx context.Context, metadataID, metadataType, itemID string) (json.RawMessage, error) {
	return s.call(ctx, "MetadataEditors/GetNavigationPropertyEntityTypes", map[string]any{
		"metadataId":   metadataID,
		"metadataType": metadataType,
		"metadataPaths": []any{
			map[string]any{"itemId": itemID, "metadataInnerPartType": "propertyType"},
		},
	})
}

// GetAvailableNavigationEntityTypes допустимые типы для нового ссылочного свойства.
func (s *Service) GetAvailableNavigationEntityTypes(ctx context.Context) (json.RawMessage, error) {
	return s.call(ctx, "MetadataEditors/GetAvailableNavigationEntityTypes", map[string]any{})
}

// editorMethod собирает имя метода редактора метаданных: MetadataEditors/{Kind}/{Collection}/{Action}.
func editorMethod(metadataType, collection, action string) string {
	return "MetadataEditors/" + kindSegment(metadataType) + "/" + collection + "/" + action
}

// kindSegment превращает «Databook» в «DatabookMetadata» (и оставляет уже полное имя).
func kindSegment(metadataType string) string {
	if strings.HasSuffix(metadataType, "Metadata") {
		return metadataType
	}
	return metadataType + "Metadata"
}

// --- проверка, публикация, NoCode ---

// Validate проверка целостности разработки (IntegrityValidation/ValidateAll).
func (s *Service) Validate(ctx context.Context) (json.RawMessage, error) {
	c, cancel := context.WithTimeout(ctx, s.deployTO)
	defer cancel()
	return s.callParams(c, "IntegrityValidation/ValidateAll", nil)
}

// Publish публикует разработку (FastDeploy/RunFastDeploy). isRedeploy=true включает
// перепубликацию с очисткой кешей. Публикация идёт минуты, таймаут отдельный.
func (s *Service) Publish(ctx context.Context, isRedeploy bool) (json.RawMessage, error) {
	c, cancel := context.WithTimeout(ctx, s.deployTO)
	defer cancel()
	return s.call(c, "FastDeploy/RunFastDeploy", map[string]any{"isRedeploy": isRedeploy})
}

// ApplyNoCode применяет сохранённые NoCode-настройки (NoCodeSettings/ApplySettings).
func (s *Service) ApplyNoCode(ctx context.Context) (json.RawMessage, error) {
	c, cancel := context.WithTimeout(ctx, s.deployTO)
	defer cancel()
	return s.call(c, "NoCodeSettings/ApplySettings", map[string]any{})
}

// --- вспомогательное ---

func orNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// plainString достаёт строку из результата, который является просто JSON-строкой
// (например, scope-строка от OpenDevelopment).
func plainString(raw json.RawMessage) (string, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s, nil
	}
	return "", fmt.Errorf("ожидалась строка в ответе Studio, получено: %s", strings.TrimSpace(string(raw)))
}

// guidFromResult достаёт GUID из результата CreateDevelopmentItem.
func guidFromResult(raw json.RawMessage) (string, error) {
	if s, ok := stringFrom(raw, "value"); ok {
		return s, nil
	}
	if s, ok := stringFrom(raw, "id"); ok {
		return s, nil
	}
	if s, ok := stringFrom(raw, "itemId"); ok {
		return s, nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil && str != "" {
		return str, nil
	}
	return "", fmt.Errorf("GUID созданного объекта не разобран из ответа Studio: %s", strings.TrimSpace(string(raw)))
}

// idFromResult достаёт itemId из результата CreateMetadataElement.
func idFromResult(raw json.RawMessage) (string, error) {
	for _, f := range []string{"itemId", "value", "id"} {
		if s, ok := stringFrom(raw, f); ok {
			return s, nil
		}
	}
	var str string
	if json.Unmarshal(raw, &str) == nil && str != "" {
		return str, nil
	}
	return "", fmt.Errorf("itemId не разобран из ответа Studio: %s", strings.TrimSpace(string(raw)))
}

func stringFrom(raw json.RawMessage, field string) (string, bool) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}
	if v, ok := m[field]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

// firstBaseType достаёт GUID первого базового типа из списка (best-effort).
func firstBaseType(raw json.RawMessage) string {
	var m struct {
		DefaultValueID string `json:"defaultValueId"`
		Types          []struct {
			ID string `json:"id"`
		} `json:"developmentItemTypes"`
	}
	if json.Unmarshal(raw, &m) == nil {
		if m.DefaultValueID != "" {
			return m.DefaultValueID
		}
		if len(m.Types) > 0 && m.Types[0].ID != "" {
			return m.Types[0].ID
		}
	}
	return ""
}
