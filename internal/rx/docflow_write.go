package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Отправка документа на ознакомление и согласование. Действия модулей RecordManagement
// и Docflow только создают задачу в черновике (task.Save() и Id), поэтому после создания
// задачу стартуем, как простую.

// AcquaintanceInput задача на ознакомление.
type AcquaintanceInput struct {
	DocumentID   int64
	PerformerIDs []int64
	Text         string
	Electronic   bool // электронное ознакомление: участник подтверждает подписью в RX
	Deadline     time.Time
}

// SendForAcquaintance создаёт и стартует задачу на ознакомление.
func (s *Service) SendForAcquaintance(ctx context.Context, in AcquaintanceInput) (int64, error) {
	if len(in.PerformerIDs) == 0 {
		return 0, errors.New("нужен хотя бы один участник ознакомления (Id сотрудника, см. rx_find_employees)")
	}
	if in.Deadline.IsZero() {
		return 0, errors.New("нужен срок ознакомления")
	}
	data, err := s.c.Action(ctx, "RecordManagement", "CreateAcquaintanceTask", map[string]any{
		"documentId": in.DocumentID, "performerIds": in.PerformerIDs, "activeText": in.Text,
		"isElectronicAcquaintance": in.Electronic, "deadline": in.Deadline.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return 0, err
	}
	return s.startCreated(ctx, data, "ознакомление")
}

// ApprovalInput задача на согласование документа.
type ApprovalInput struct {
	DocumentID  int64
	ApproverIDs []int64
	Text        string
	Deadline    *time.Time
	// ByRule согласование по регламенту: маршрут строит правило RX для вида документа,
	// ApproverIDs становятся дополнительными согласующими. Иначе свободное согласование.
	ByRule      bool
	SignatoryID int64
}

// SendForApproval создаёт и стартует задачу на согласование.
func (s *Service) SendForApproval(ctx context.Context, in ApprovalInput) (int64, error) {
	var data []byte
	var err error
	if in.ByRule {
		p := map[string]any{"documentId": in.DocumentID, "text": in.Text, "addApproverIds": nz(in.ApproverIDs)}
		if in.SignatoryID != 0 {
			p["signatoryId"] = in.SignatoryID
		}
		data, err = s.c.Action(ctx, "Docflow", "CreateApprovalTask", p)
	} else {
		if len(in.ApproverIDs) == 0 {
			return 0, errors.New("для свободного согласования нужен хотя бы один согласующий (Id сотрудника, см. rx_find_employees)")
		}
		p := map[string]any{"documentId": in.DocumentID, "text": in.Text, "approverIds": in.ApproverIDs}
		if in.Deadline != nil {
			p["deadline"] = in.Deadline.UTC().Format(time.RFC3339)
		}
		data, err = s.c.Action(ctx, "Docflow", "CreateFreeApprovalTask", p)
	}
	if err != nil {
		return 0, err
	}
	return s.startCreated(ctx, data, "согласование")
}

// startCreated разбирает Id созданной задачи и стартует её. Если старт не прошёл,
// возвращает Id вместе с ошибкой: черновик остался в RX, его можно стартовать там
// или прекратить.
func (s *Service) startCreated(ctx context.Context, data []byte, what string) (int64, error) {
	id, err := actionInt(data)
	if err != nil {
		return 0, fmt.Errorf("задача на %s создана, но Id не разобран: %w", what, err)
	}
	if _, err := s.c.Action(ctx, "Docflow", "StartTask", map[string]any{"taskId": id}); err != nil {
		return id, fmt.Errorf("задача на %s #%d создана, но не стартовала: %w. Черновик остался в RX: стартуйте его там или прекратите через rx_abort_task", what, id, err)
	}
	return id, nil
}

// CreateArticle создаёт статью базы знаний в области и кладёт текст первой версией.
// Вид документа и состояние RX проставляет сам при создании.
func (s *Service) CreateArticle(ctx context.Context, title, text string, areaID int64) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, errors.New("нужно название статьи")
	}
	if strings.TrimSpace(text) == "" {
		return 0, errors.New("нужен текст статьи")
	}
	if areaID == 0 {
		return 0, errors.New("нужна область знаний (Id из rx_kb_areas)")
	}
	data, err := s.c.Create(ctx, "IMemoArticles", map[string]any{
		"Name":  title,
		"Areas": []any{map[string]any{"Area": map[string]any{"Id": areaID}}},
	})
	if err != nil {
		return 0, err
	}
	var created struct {
		ID int64 `json:"Id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == 0 {
		return 0, errors.New("статья создана, но Id не разобран")
	}
	if _, err := s.c.Action(ctx, "MemoRX", "CreateMemoVersion", map[string]any{"memoId": created.ID, "content": text}); err != nil {
		// Пустая статья без текста никому не нужна: убираем её штатным действием.
		_, _ = s.c.Action(ctx, "MemoRX", "DeleteEmptyArticleById", map[string]any{"memoId": created.ID})
		return 0, fmt.Errorf("статья создалась, но текст не записан (%w); пустую статью удалил", err)
	}
	return created.ID, nil
}

// UpdateArticle меняет название и (или) текст статьи. Текст записывается новой версией:
// прежняя остаётся в истории статьи.
func (s *Service) UpdateArticle(ctx context.Context, id int64, title, text string) error {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(text) == "" {
		return errors.New("нечего менять: передайте title или text")
	}
	if t := strings.TrimSpace(title); t != "" {
		if _, err := s.c.Action(ctx, "MemoRX", "UpdateArticleTitle", map[string]any{"memoId": id, "title": t}); err != nil {
			return err
		}
	}
	if strings.TrimSpace(text) != "" {
		if _, err := s.c.Action(ctx, "MemoRX", "CreateMemoVersion", map[string]any{"memoId": id, "content": text}); err != nil {
			return err
		}
	}
	return nil
}

// Набор сущности eTMF-документа в сервисе интеграции. Имя набора и имена свойств
// берутся из решения eTMF (клинические исследования) и на стендах могут отличаться:
// набор переопределяется переменной окружения RXMCP_ETMF_SET, имена свойств — прямо
// в body() ниже. Имена своего стенда можно посмотреть через rx_find_entity и
// rx_describe_entity (набор $metadata).
const etmfSetDefault = "IEtmfDocuments"

func etmfSet() string {
	if v := strings.TrimSpace(os.Getenv("RXMCP_ETMF_SET")); v != "" {
		return v
	}
	return etmfSetDefault
}

// EtmfDocumentInput поля карточки eTMF-документа, заполняемые при создании.
// Ссылки передаются Id: объект, на который идёт ссылка, уже должен существовать
// в системе. Дата создания, имя исследования и состояние жизненного цикла
// (Состояние, Согласование, Этап исследования) проставляет сам RX — их в теле
// запроса не передаём.
type EtmfDocumentInput struct {
	Name                 string    // Имя
	ArtifactID           int64     // Артефакт
	Version              string    // Версия
	ZoneID               int64     // Зона (01. Управление исследованием и т.д.)
	SectionID            int64     // Секция (Команда исследования и т.д.)
	EffectiveDate        time.Time // Дата введения в действие
	ICHCode              string    // Код ICH (5.6 и т.д.)
	StudyTypeID          int64     // Тип исследования
	SponsorDocument      bool      // флажок «Sponsor Document»
	InvestigatorDocument bool      // флажок «Investigator Document»
	ProcessID            int64     // Процесс
	ProcessNumber        string    // Номер процесса
	StudyID              int64     // ИД исследования
	PhaseID              int64     // Фаза исследования
	CountryID            int64     // Страна
	CenterID             int64     // Центр (площадка)
	OurOrgID             int64     // Наша организация
	DepartmentID         int64     // Подразделение
	AuthorID             int64     // Автор; 0 = текущий пользователь
	SignatoryID          int64     // Подписант
}

// body собирает тело POST-запроса: имя и только заполненные поля. Ссылочные
// свойства заполняются объектом {"Id": …}, как того требует сервис интеграции.
func (in EtmfDocumentInput) body(loc *time.Location) map[string]any {
	b := map[string]any{"Name": in.Name}
	for k, v := range map[string]string{
		"Version":       in.Version,
		"ICHCode":       in.ICHCode,
		"ProcessNumber": in.ProcessNumber,
	} {
		if v = strings.TrimSpace(v); v != "" {
			b[k] = v
		}
	}
	for k, id := range map[string]int64{
		"Artifact":   in.ArtifactID,
		"Zone":       in.ZoneID,
		"Section":    in.SectionID,
		"StudyType":  in.StudyTypeID,
		"Process":    in.ProcessID,
		"Study":      in.StudyID,
		"Phase":      in.PhaseID,
		"Country":    in.CountryID,
		"Center":     in.CenterID,
		"OurOrg":     in.OurOrgID,
		"Department": in.DepartmentID,
		"Author":     in.AuthorID,
		"Signatory":  in.SignatoryID,
	} {
		if id != 0 {
			b[k] = map[string]any{"Id": id}
		}
	}
	if in.SponsorDocument {
		b["SponsorDocument"] = true
	}
	if in.InvestigatorDocument {
		b["InvestigatorDocument"] = true
	}
	if !in.EffectiveDate.IsZero() {
		// Поля-даты RX хранит полночью в поясе сервера: пишем так же,
		// иначе день может сдвинуться на сутки.
		d := in.EffectiveDate
		b["EffectiveDate"] = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)
	}
	return b
}

// CreateEtmfDocument создаёт карточку eTMF-документа (EtmfDocument) через OData
// и возвращает Id созданного документа. Заголовки Content-Type и Accept клиент
// OData проставляет сам; ответ приходит с созданной сущностью, из него берётся Id.
func (s *Service) CreateEtmfDocument(ctx context.Context, in EtmfDocumentInput) (int64, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return 0, errors.New("нужно имя eTMF-документа")
	}
	loc := s.F.Loc
	if loc == nil {
		loc = time.UTC
	}
	data, err := s.c.Create(ctx, etmfSet(), in.body(loc))
	if err != nil {
		return 0, fmt.Errorf("создание eTMF-документа: %w", err)
	}
	var created struct {
		ID int64 `json:"Id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == 0 {
		return 0, errors.New("eTMF-документ создан, но Id из ответа не разобран")
	}
	return created.ID, nil
}
