package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/rx"
)

type acquaintanceIn struct {
	DocumentID   int64   `json:"document_id" jsonschema:"Id документа: число после # в строке rx_find_documents"`
	PerformerIDs []int64 `json:"performer_ids" jsonschema:"Id сотрудников, которые должны ознакомиться (rx_find_employees)"`
	Deadline     string  `json:"deadline" jsonschema:"срок ознакомления, обязателен: ГГГГ-ММ-ДД, ГГГГ-ММ-ДДTЧЧ:ММ, «завтра» или «через 3 дня»"`
	Text         string  `json:"text,omitempty" jsonschema:"текст задачи: что и зачем прочитать"`
	Electronic   bool    `json:"electronic,omitempty" jsonschema:"true = электронное ознакомление: участник подтверждает его подписью в RX; по умолчанию обычное"`
}

type approvalIn struct {
	DocumentID  int64   `json:"document_id" jsonschema:"Id документа: число после # в строке rx_find_documents"`
	ApproverIDs []int64 `json:"approver_ids,omitempty" jsonschema:"Id согласующих (rx_find_employees); для свободного согласования обязательны, для mode=rule это дополнительные согласующие сверх правила"`
	Mode        string  `json:"mode,omitempty" jsonschema:"free (по умолчанию) = свободное согласование с указанными людьми; rule = по регламенту: маршрут строит правило согласования RX для вида документа"`
	Text        string  `json:"text,omitempty" jsonschema:"текст задачи: что согласовать и на что обратить внимание"`
	Deadline    string  `json:"deadline,omitempty" jsonschema:"срок свободного согласования: ГГГГ-ММ-ДД, «завтра», «через 3 дня»; в mode=rule сроки задаёт правило"`
	SignatoryID int64   `json:"signatory_id,omitempty" jsonschema:"для mode=rule: Id подписанта, если правило его не определяет само"`
}

type createArticleIn struct {
	Title  string `json:"title" jsonschema:"название статьи"`
	Text   string `json:"text" jsonschema:"текст статьи в markdown: заголовки ##, списки, таблицы, ссылки"`
	AreaID int64  `json:"area_id" jsonschema:"Id области знаний из rx_kb_areas"`
}

type updateArticleIn struct {
	ID    int64  `json:"id" jsonschema:"Id статьи из rx_kb_search"`
	Title string `json:"title,omitempty" jsonschema:"новое название; не передавайте, если не меняется"`
	Text  string `json:"text,omitempty" jsonschema:"новый текст целиком в markdown; запишется новой версией, прежняя останется в истории"`
}

type createEtmfDocumentIn struct {
	Name                 string `json:"name" jsonschema:"имя документа, обязательно; например «План управления исследованием (Trial Management Plan) 17.09.2026 Нейро-Когнитив-Фаза I»"`
	ArtifactID           int64  `json:"artifact_id,omitempty" jsonschema:"Id артефакта (справочник артефактов eTMF, rx_find_entity + rx_query)"`
	Version              string `json:"version,omitempty" jsonschema:"версия документа, например 1"`
	ZoneID               int64  `json:"zone_id,omitempty" jsonschema:"Id зоны eTMF, например «01. Управление исследованием (Trial Management)» (справочник зон, rx_find_entity + rx_query)"`
	SectionID            int64  `json:"section_id,omitempty" jsonschema:"Id секции eTMF, например «Команда исследования (Trial Team)» (справочник секций, rx_find_entity + rx_query)"`
	EffectiveDate        string `json:"effective_date,omitempty" jsonschema:"дата введения в действие: ГГГГ-ММ-ДД или ДД.ММ.ГГГГ"`
	ICHCode              string `json:"ich_code,omitempty" jsonschema:"код ICH, например 5.6"`
	StudyTypeID          int64  `json:"study_type_id,omitempty" jsonschema:"Id типа исследования, например «Немедицинские препараты» (справочник)"`
	SponsorDocument      bool   `json:"sponsor_document,omitempty" jsonschema:"true = установить флажок «Sponsor Document» (документ спонсора)"`
	InvestigatorDocument bool   `json:"investigator_document,omitempty" jsonschema:"true = установить флажок «Investigator Document» (документ исследователя)"`
	ProcessID            int64  `json:"process_id,omitempty" jsonschema:"Id процесса, например «Разработать стратегию управления исследованием» (справочник процессов)"`
	ProcessNumber        string `json:"process_number,omitempty" jsonschema:"номер процесса, например 12"`
	StudyID              int64  `json:"study_id,omitempty" jsonschema:"Id исследования (ИД исследования, например 102)"`
	PhaseID              int64  `json:"phase_id,omitempty" jsonschema:"Id фазы исследования, например «Закрытие» (справочник)"`
	CountryID            int64  `json:"country_id,omitempty" jsonschema:"Id страны, например «Российская Федерация» (справочник стран)"`
	CenterID             int64  `json:"center_id,omitempty" jsonschema:"Id центра/площадки исследования, например «НМИЦ Кардиологии»"`
	OurOrgID             int64  `json:"our_org_id,omitempty" jsonschema:"Id нашей организации (Наша орг.), например АО «ФармЗавод «БиоСинтез»"`
	DepartmentID         int64  `json:"department_id,omitempty" jsonschema:"Id подразделения, например «Лаборатория контроля качества (QC)»"`
	AuthorID             int64  `json:"author_id,omitempty" jsonschema:"Id автора-сотрудника (rx_find_employees); не передавайте, чтобы записался текущий пользователь"`
	SignatoryID          int64  `json:"signatory_id,omitempty" jsonschema:"Id подписанта-сотрудника (rx_find_employees)"`
}

// parseEtmfDate разбирает дату введения в действие: модели обычно дают ISO,
// человек в переписке — ДД.ММ.ГГГГ.
func parseEtmfDate(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range []string{"2006-01-02", "02.01.2006"} {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("дата введения в действие %q: ожидается ГГГГ-ММ-ДД или ДД.ММ.ГГГГ", s)
}

// summary пересказывает заполняемые поля карточки для подтверждения: только то,
// что реально передали, словами с самой карточки eTMF.
func (in createEtmfDocumentIn) summary(f *rx.Formatter) []string {
	var out []string
	if v := strings.TrimSpace(in.Version); v != "" {
		out = append(out, "версия "+v)
	}
	for _, p := range []struct {
		label string
		id    int64
	}{
		{"артефакт", in.ArtifactID},
		{"зона", in.ZoneID},
		{"секция", in.SectionID},
		{"тип исследования", in.StudyTypeID},
		{"процесс", in.ProcessID},
		{"исследование", in.StudyID},
		{"фаза", in.PhaseID},
		{"страна", in.CountryID},
		{"центр", in.CenterID},
		{"наша орг.", in.OurOrgID},
		{"подразделение", in.DepartmentID},
		{"автор", in.AuthorID},
		{"подписант", in.SignatoryID},
	} {
		if p.id != 0 {
			out = append(out, fmt.Sprintf("%s #%d", p.label, p.id))
		}
	}
	if in.EffectiveDate != "" {
		if t, err := parseEtmfDate(in.EffectiveDate, f.Loc); err == nil {
			out = append(out, "дата введения в действие "+f.Date(&t))
		} else {
			out = append(out, "дата введения в действие "+in.EffectiveDate)
		}
	}
	if v := strings.TrimSpace(in.ICHCode); v != "" {
		out = append(out, "код ICH "+v)
	}
	if v := strings.TrimSpace(in.ProcessNumber); v != "" {
		out = append(out, "номер процесса "+v)
	}
	if in.SponsorDocument {
		out = append(out, "флажок «Sponsor Document»")
	}
	if in.InvestigatorDocument {
		out = append(out, "флажок «Investigator Document»")
	}
	return out
}

// docLabel «#Id «название»» для форм подтверждения; если документ не прочитать,
// RX всё равно ответит ошибкой на само действие.
func (s *Server) docLabel(ctx context.Context, id int64) string {
	if d, err := s.svc.GetDocument(ctx, id); err == nil {
		return fmt.Sprintf("#%d «%s»", d.ID, d.Name)
	}
	return fmt.Sprintf("#%d", id)
}

func (s *Server) registerDocflowWrite() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_send_for_acquaintance",
		Description: "Отправить документ на ознакомление сотрудникам: создаёт и стартует задачу на ознакомление, участники получают задания. Нужны Id документа (rx_find_documents), Id сотрудников (rx_find_employees) и срок. electronic=true требует от участников подтвердить ознакомление подписью в RX. Меняет данные в RX. Перед вызовом перескажите пользователю документ, участников и срок.",
		Annotations: rw("Отправить на ознакомление", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in acquaintanceIn) (*mcp.CallToolResult, any, error) {
		if in.Deadline == "" {
			return fail(errors.New("нужен срок ознакомления (deadline): спросите у пользователя или предложите через 3 рабочих дня"))
		}
		dl, err := parseDeadline(in.Deadline, s.svc.F.Loc)
		if err != nil {
			return fail(err)
		}
		doc := s.docLabel(ctx, in.DocumentID)
		kind := "ознакомление"
		if in.Electronic {
			kind = "электронное ознакомление (с подписью)"
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Отправить документ %s на %s сотрудникам %v, срок %s?", doc, kind, in.PerformerIDs, s.svc.F.DateTime(&dl))); err != nil || !ok {
			return declined(err)
		}
		id, err := s.svc.SendForAcquaintance(ctx, rx.AcquaintanceInput{DocumentID: in.DocumentID, PerformerIDs: in.PerformerIDs, Text: in.Text, Electronic: in.Electronic, Deadline: dl})
		if err != nil {
			return fail(err)
		}
		s.log.Info("acquaintance task started", "id", id, "document", in.DocumentID)
		return text(fmt.Sprintf("Документ %s отправлен на ознакомление: задача #%d. Ход ознакомления: rx_get_task.", doc, id)), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_send_for_approval",
		Description: "Отправить документ на согласование: создаёт и стартует задачу. mode=free (по умолчанию) это свободное согласование с указанными людьми и сроком. mode=rule это согласование по регламенту: маршрут, этапы и сроки строит правило согласования RX для вида документа, approver_ids добавляются сверх правила; если правила нет, RX откажет. Подпись документа этот инструмент не ставит. Меняет данные в RX. Перед вызовом перескажите пользователю документ, режим и согласующих.",
		Annotations: rw("Отправить на согласование", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in approvalIn) (*mcp.CallToolResult, any, error) {
		mode := strings.ToLower(strings.TrimSpace(in.Mode))
		byRule := false
		switch mode {
		case "", "free", "свободное":
		case "rule", "регламент", "по регламенту":
			byRule = true
		default:
			return fail(errors.New("mode: free (свободное согласование) или rule (по регламенту)"))
		}
		var dl *time.Time
		if in.Deadline != "" && !byRule {
			t, err := parseDeadline(in.Deadline, s.svc.F.Loc)
			if err != nil {
				return fail(err)
			}
			dl = &t
		}
		doc := s.docLabel(ctx, in.DocumentID)
		msg := fmt.Sprintf("Отправить документ %s на свободное согласование: %v", doc, in.ApproverIDs)
		if dl != nil {
			msg += ", срок " + s.svc.F.DateTime(dl)
		}
		if byRule {
			msg = fmt.Sprintf("Отправить документ %s на согласование по регламенту (маршрут по правилу RX)", doc)
			if len(in.ApproverIDs) > 0 {
				msg += fmt.Sprintf(", дополнительно %v", in.ApproverIDs)
			}
			if in.SignatoryID != 0 {
				msg += fmt.Sprintf(", подписант %d", in.SignatoryID)
			}
		}
		if ok, err := s.confirm(ctx, req, msg+"?"); err != nil || !ok {
			return declined(err)
		}
		id, err := s.svc.SendForApproval(ctx, rx.ApprovalInput{DocumentID: in.DocumentID, ApproverIDs: in.ApproverIDs, Text: in.Text, Deadline: dl, ByRule: byRule, SignatoryID: in.SignatoryID})
		if err != nil {
			return fail(err)
		}
		s.log.Info("approval task started", "id", id, "document", in.DocumentID, "rule", byRule)
		return text(fmt.Sprintf("Документ %s отправлен на согласование: задача #%d. Ход согласования: rx_get_task.", doc, id)), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_article",
		Description: "Создать статью базы знаний в области: название и текст в markdown. Статья создаётся от имени пользователя и видна по правам области. Нужен Id области из rx_kb_areas. Перед созданием проверьте через rx_kb_search, что такой статьи ещё нет. Меняет данные в RX. Перед вызовом покажите пользователю название и начало текста.",
		Annotations: rw("Создать статью", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createArticleIn) (*mcp.CallToolResult, any, error) {
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Создать статью «%s» в области %d (%d символов текста)?", in.Title, in.AreaID, len([]rune(in.Text)))); err != nil || !ok {
			return declined(err)
		}
		id, err := s.svc.CreateArticle(ctx, in.Title, in.Text, in.AreaID)
		if err != nil {
			return fail(err)
		}
		s.log.Info("article created", "id", id)
		return text(fmt.Sprintf("Статья «%s» создана: #%d. Посмотреть: rx_kb_article.", in.Title, id)), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_update_article",
		Description: "Изменить статью базы знаний: название и (или) текст целиком в markdown. Новый текст записывается новой версией, прежняя остаётся в истории статьи. Сначала прочитайте статью через rx_kb_article и правьте её текст, а не пишите заново. Меняет данные в RX. Перед вызовом перескажите пользователю, что меняется.",
		Annotations: rw("Изменить статью", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateArticleIn) (*mcp.CallToolResult, any, error) {
		var what []string
		if in.Title != "" {
			what = append(what, fmt.Sprintf("название на «%s»", in.Title))
		}
		if in.Text != "" {
			what = append(what, fmt.Sprintf("текст новой версией (%d символов)", len([]rune(in.Text))))
		}
		if len(what) == 0 {
			return fail(errors.New("нечего менять: передайте title или text"))
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Изменить статью #%d: %s?", in.ID, strings.Join(what, ", "))); err != nil || !ok {
			return declined(err)
		}
		if err := s.svc.UpdateArticle(ctx, in.ID, in.Title, in.Text); err != nil {
			return fail(err)
		}
		s.log.Info("article updated", "id", in.ID)
		return text(fmt.Sprintf("Статья #%d изменена: %s.", in.ID, strings.Join(what, ", "))), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_etmf_document",
		Description: "Создать карточку eTMF-документа (EtmfDocument) в Directum RX и заполнить поля из тела запроса: имя, артефакт, версия, зона, секция, дата введения в действие, код ICH, тип исследования, флажки «Sponsor Document» и «Investigator Document», процесс и его номер, исследование, фаза, страна, центр, наша организация, подразделение, автор, подписант. Ссылочные поля передавайте Id сущностей: справочники (зоны, секции, артефакты, процессы, типы исследования, фазы, страны, центры, организации, подразделения) ищите через rx_find_entity и rx_query, сотрудников — через rx_find_employees. Объект ссылки должен уже существовать. Дата создания, имя исследования и состояние жизненного цикла проставляются автоматически. Меняет данные в RX. Перед вызовом перескажите пользователю, что попадёт в карточку.",
		Annotations: rw("Создать eTMF-документ", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createEtmfDocumentIn) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Name) == "" {
			return fail(errors.New("нужно имя документа (name)"))
		}
		var eff time.Time
		if in.EffectiveDate != "" {
			t, err := parseEtmfDate(in.EffectiveDate, s.svc.F.Loc)
			if err != nil {
				return fail(err)
			}
			eff = t
		}

		msg := fmt.Sprintf("Создать eTMF-документ «%s»", in.Name)
		if fields := in.summary(&s.svc.F); len(fields) > 0 {
			msg += " с полями: " + strings.Join(fields, ", ")
		}
		if ok, err := s.confirm(ctx, req, msg+"?"); err != nil || !ok {
			return declined(err)
		}

		id, err := s.svc.CreateEtmfDocument(ctx, rx.EtmfDocumentInput{
			Name:                 in.Name,
			ArtifactID:           in.ArtifactID,
			Version:              in.Version,
			ZoneID:               in.ZoneID,
			SectionID:            in.SectionID,
			EffectiveDate:        eff,
			ICHCode:              in.ICHCode,
			StudyTypeID:          in.StudyTypeID,
			SponsorDocument:      in.SponsorDocument,
			InvestigatorDocument: in.InvestigatorDocument,
			ProcessID:            in.ProcessID,
			ProcessNumber:        in.ProcessNumber,
			StudyID:              in.StudyID,
			PhaseID:              in.PhaseID,
			CountryID:            in.CountryID,
			CenterID:             in.CenterID,
			OurOrgID:             in.OurOrgID,
			DepartmentID:         in.DepartmentID,
			AuthorID:             in.AuthorID,
			SignatoryID:          in.SignatoryID,
		})
		if err != nil {
			return fail(err)
		}
		s.log.Info("etmf document created", "id", id, "name", in.Name)
		return text(fmt.Sprintf("eTMF-документ создан: #%d «%s». Прочитать карточку можно через rx_query по набору eTMF-документов.", id, in.Name)), nil, nil
	})
}
