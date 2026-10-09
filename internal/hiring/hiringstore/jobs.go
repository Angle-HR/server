package hiringstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/questions"
)

// Aliases keep the store's API in terms of the shared types.
type (
	JobRecord    = hiringtypes.JobRecord
	CreateInput  = hiringtypes.CreateInput
	UpdateResult = hiringtypes.UpdateResult
	ListFilter   = hiringtypes.ListFilter
	ListItem     = hiringtypes.ListItem
	ListPerson   = hiringtypes.ListPerson
)

// Page size bounds of the jobs list.
const (
	DefaultListLimit = hiringtypes.DefaultListLimit
	MaxListLimit     = hiringtypes.MaxListLimit
)

// EncodeCursor and DecodeCursor are re-exported from hiringtypes.
var (
	EncodeCursor = hiringtypes.EncodeCursor
	DecodeCursor = hiringtypes.DecodeCursor
)

const tsLayout = `YYYY-MM-DD"T"HH24:MI:SS.US"Z"`

const nextJobNumberSQL = `
INSERT INTO hiring.org_counters (tenant_id, next_job_number) VALUES ($1::uuid, 2)
ON CONFLICT (tenant_id) DO UPDATE SET next_job_number = hiring.org_counters.next_job_number + 1
RETURNING next_job_number - 1`

// revision starts at 0 because the create transaction's details write increments it,
// so the revision the client first sees is 1.
const insertJobSQL = `
INSERT INTO hiring.job_postings (tenant_id, created_by, job_number, revision)
VALUES ($1::uuid, $2::uuid, $3, 0)
RETURNING id::text`

const updateJobSQL = `
UPDATE hiring.job_postings SET
    title = NULLIF($3, ''), department_id = NULLIF($4, '')::uuid, closing_date = NULLIF($5, '')::date,
    location_mode = NULLIF($6, ''), location_text = NULLIF($7, ''), use_company_address = $8,
    workplace_type = NULLIF($9, ''), travel_frequency = NULLIF($10, ''), visa_sponsorship = NULLIF($11, ''),
    description_sections = $12::jsonb, description_text = NULLIF($13, ''), industry_id = NULLIF($14, '')::uuid,
    custom_industry = NULLIF($15, ''), employment_type = NULLIF($16, ''),
    seniority_level_id = NULLIF($17, '')::uuid, experience_range_id = NULLIF($18, '')::uuid,
    pay_type = NULLIF($19, ''), pay_min = $20::bigint, pay_max = $21::bigint,
    pay_currency = NULLIF($22, ''), pay_period = NULLIF($23, ''), pay_visible = $24,
    show_on_career_page = $25, lawful_basis = NULLIF($26, ''), lia_reference = NULLIF($27, ''),
    retention_months = $28, assessment_url = NULLIF($29, ''), title_key = $30, location_key = $31,
    current_step = $32, completed_sections = $33::text[], revision = revision + 1
WHERE id = $1::uuid AND tenant_id = $2::uuid AND deleted_at IS NULL
RETURNING revision`

const deleteMarketsSQL = `DELETE FROM hiring.job_markets WHERE job_id = $1::uuid`

const insertMarketsSQL = `
INSERT INTO hiring.job_markets (job_id, tenant_id, market_code, subdivision, city, timezone)
SELECT $1::uuid, $2::uuid, t.m, t.s, t.c, t.z
FROM unnest($3::text[], $4::text[], $5::text[], $6::text[]) AS t(m, s, c, z)`

const deleteSkillsSQL = `DELETE FROM hiring.job_skills WHERE job_id = $1::uuid`

const insertSkillsSQL = `
INSERT INTO hiring.job_skills (job_id, tenant_id, skill_id, custom_label, position)
SELECT $1::uuid, $2::uuid, NULLIF(t.id, '')::uuid, NULLIF(t.label, ''), t.pos
FROM unnest($3::text[], $4::text[], $5::int[]) AS t(id, label, pos)`

const loadJobSQL = `
SELECT j.id::text, j.job_number, j.created_by::text, j.status, j.current_step, j.completed_sections, j.revision,
       COALESCE(d.name, ''),
       to_char(j.created_at AT TIME ZONE 'UTC', '` + tsLayout + `'),
       to_char(j.updated_at AT TIME ZONE 'UTC', '` + tsLayout + `'),
       COALESCE(to_char(j.published_at AT TIME ZONE 'UTC', '` + tsLayout + `'), ''),
       COALESCE(j.title, ''), COALESCE(j.department_id::text, ''), COALESCE(to_char(j.closing_date, 'YYYY-MM-DD'), ''),
       COALESCE(j.location_mode, ''), COALESCE(j.location_text, ''), j.use_company_address,
       COALESCE(j.workplace_type, ''), COALESCE(j.travel_frequency, ''), COALESCE(j.visa_sponsorship, ''),
       j.description_sections, COALESCE(j.industry_id::text, ''), COALESCE(j.custom_industry, ''),
       COALESCE(j.employment_type, ''), COALESCE(j.seniority_level_id::text, ''),
       COALESCE(j.experience_range_id::text, ''),
       COALESCE(j.pay_type, ''), j.pay_min, j.pay_max, COALESCE(j.pay_currency, ''), COALESCE(j.pay_period, ''),
       j.pay_visible, j.show_on_career_page, COALESCE(j.lawful_basis, ''), COALESCE(j.lia_reference, ''),
       j.retention_months, COALESCE(j.assessment_url, ''),
       COALESCE(j.public_id, ''), COALESCE(to_char(j.dpia_confirmed_at AT TIME ZONE 'UTC', '` + tsLayout + `'), '')
FROM hiring.job_postings j
LEFT JOIN hiring.departments d ON d.id = j.department_id
WHERE j.id = $1::uuid AND j.tenant_id = $2::uuid AND j.deleted_at IS NULL`

const lockSuffix = ` FOR UPDATE OF j`

const loadMarketsSQL = `
SELECT market_code, subdivision, city, timezone FROM hiring.job_markets
WHERE job_id = $1::uuid ORDER BY market_code, subdivision, city, timezone`

const loadSkillsSQL = `
SELECT COALESCE(skill_id::text, ''), COALESCE(custom_label, '') FROM hiring.job_skills
WHERE job_id = $1::uuid ORDER BY position, id`

const loadFormSQL = `
SELECT q.id::text, q.section, q.position, q.type, q.label, COALESCE(q.description, ''),
       COALESCE(q.helper_text, ''), q.required, q.config, COALESCE(q.system_key, ''), q.locked, q.knockout,
       COALESCE(q.special_category, '')
FROM hiring.form_questions q
JOIN hiring.application_forms f ON f.id = q.form_id
WHERE f.job_id = $1::uuid ORDER BY q.position`

const loadFormRevisionSQL = `SELECT revision FROM hiring.application_forms WHERE job_id = $1::uuid`

const loadDeclarationsSQL = `
SELECT category, legal_condition, purpose FROM hiring.special_category_declarations
WHERE job_id = $1::uuid ORDER BY category`

const upsertFormSQL = `
INSERT INTO hiring.application_forms (job_id, tenant_id) VALUES ($1::uuid, $2::uuid)
ON CONFLICT (job_id) DO UPDATE SET revision = hiring.application_forms.revision + 1, updated_at = now()
RETURNING id::text`

const existingQuestionIDsSQL = `SELECT id::text FROM hiring.form_questions WHERE form_id = $1::uuid`

const deleteQuestionsSQL = `DELETE FROM hiring.form_questions WHERE form_id = $1::uuid`

const insertQuestionSQL = `
INSERT INTO hiring.form_questions
    (id, form_id, tenant_id, section, position, type, label, description, helper_text, required, config,
     system_key, locked, knockout, special_category)
VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2::uuid, $3::uuid, $4, $5, $6, $7, NULLIF($8, ''),
        NULLIF($9, ''), $10, $11::jsonb, NULLIF($12, ''), $13, $14::jsonb, NULLIF($15, ''))`

const deleteDeclarationsSQL = `DELETE FROM hiring.special_category_declarations WHERE job_id = $1::uuid`

const insertDeclarationSQL = `
INSERT INTO hiring.special_category_declarations
    (job_id, tenant_id, category, legal_condition, purpose, declared_by, retention_until, lawful_basis,
     collection_purpose)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::uuid, now() + make_interval(months => $7), $8, $5)`

const insertAuditSQL = `
INSERT INTO hiring.audit_events (job_id, tenant_id, actor_id, action, diff)
VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4, $5::jsonb)`

// CreateJob allocates the next job number, stores the draft with its form and records the creation.
func (s *Store) CreateJob(ctx context.Context, in *CreateInput) (*JobRecord, error) {
	var id string
	err := s.inTenantTx(ctx, in.TenantID, func(tx pgx.Tx) error {
		var number int
		if err := tx.QueryRow(ctx, nextJobNumberSQL, in.TenantID).Scan(&number); err != nil {
			return fmt.Errorf("hiringstore: next job number: %w", err)
		}
		if err := tx.QueryRow(ctx, insertJobSQL, in.TenantID, in.UserID, number).Scan(&id); err != nil {
			return fmt.Errorf("hiringstore: insert job: %w", mapPgError(err))
		}
		res := &UpdateResult{
			Details: &in.Details, Completed: in.Completed, Step: in.Step, Form: &in.Form, Decls: &in.Decls,
			AuditAction: "job.created",
		}
		return applyUpdate(ctx, tx, in.TenantID, in.UserID, id, res)
	})
	if err != nil {
		return nil, err
	}
	return s.GetJob(ctx, in.TenantID, id)
}

// GetJob returns a job with its form, or ErrNotFound.
func (s *Store) GetJob(ctx context.Context, tenantID, id string) (*JobRecord, error) {
	var rec *JobRecord
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		r, err := loadRecord(ctx, tx, tenantID, id, false)
		rec = r
		return err
	})
	return rec, err
}

// UpdateJob locks the job, checks ifMatch against its revision (0 skips the check), lets fn decide the change
// from the current state, and writes it. The whole read-decide-write runs under one row lock, so two editors
// cannot overwrite each other.
func (s *Store) UpdateJob(
	ctx context.Context, tenantID, actorID, id string, ifMatch int,
	fn func(cur *JobRecord) (*UpdateResult, error),
) (*JobRecord, error) {
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := loadRecord(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if ifMatch != 0 && cur.Job.Revision != ifMatch {
			return &StaleRevisionError{Current: cur.Job.Revision}
		}
		res, err := fn(cur)
		if err != nil {
			return err
		}
		if res == nil {
			return nil
		}
		return applyUpdate(ctx, tx, tenantID, actorID, id, res)
	})
	if err != nil {
		return nil, err
	}
	return s.GetJob(ctx, tenantID, id)
}

// applyUpdate writes an UpdateResult inside the caller's transaction.
func applyUpdate(ctx context.Context, tx pgx.Tx, tenantID, actorID, id string, res *UpdateResult) error {
	if res.Details != nil {
		if err := writeDetails(ctx, tx, tenantID, id, res); err != nil {
			return err
		}
	}
	if res.Form != nil {
		if err := writeForm(ctx, tx, tenantID, actorID, id, res); err != nil {
			return err
		}
	}
	if err := applyPhase2(ctx, tx, tenantID, actorID, id, res); err != nil {
		return err
	}
	if res.AuditAction != "" {
		diff, err := json.Marshal(nonNil(res.AuditDiff))
		if err != nil {
			return fmt.Errorf("hiringstore: encode audit diff: %w", err)
		}
		if _, err = tx.Exec(ctx, insertAuditSQL, id, tenantID, actorID, res.AuditAction, diff); err != nil {
			return fmt.Errorf("hiringstore: audit: %w", err)
		}
	}
	return nil
}

func nonNil(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func writeDetails(ctx context.Context, tx pgx.Tx, tenantID, id string, res *UpdateResult) error {
	d := res.Details
	sections, err := json.Marshal(nonNilSections(d.Description))
	if err != nil {
		return fmt.Errorf("hiringstore: encode description: %w", err)
	}
	completed := res.Completed
	if completed == nil {
		completed = []string{}
	}
	var revision int
	err = tx.QueryRow(ctx, updateJobSQL,
		id, tenantID, d.Title, d.DepartmentID, d.ClosingDate, d.LocationMode, d.LocationText, d.UseCompanyAddress,
		d.WorkplaceType, d.TravelFrequency, d.VisaSponsorship, sections, jobs.PlainText(d.Description),
		d.IndustryID, d.CustomIndustry, d.EmploymentType, d.SeniorityLevelID, d.ExperienceRangeID,
		d.Pay.Type, d.Pay.Min, d.Pay.Max, d.Pay.Currency, d.Pay.Period, d.Pay.Visible,
		d.ShowOnCareerPage, d.LawfulBasis, d.LIAReference, d.RetentionMonths, d.AssessmentURL,
		jobs.NormaliseTitle(d.Title), jobs.LocationKey(d.LocationMode, d.Markets), res.Step, completed,
	).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("hiringstore: update job: %w", mapPgError(err))
	}

	if _, err = tx.Exec(ctx, deleteMarketsSQL, id); err != nil {
		return fmt.Errorf("hiringstore: clear markets: %w", err)
	}
	if len(d.Markets) > 0 {
		codes, subs, cities, zones := make([]string, 0, len(d.Markets)), []string{}, []string{}, []string{}
		for _, m := range d.Markets {
			codes, subs = append(codes, m.MarketCode), append(subs, m.Subdivision)
			cities, zones = append(cities, m.City), append(zones, m.Timezone)
		}
		if _, err = tx.Exec(ctx, insertMarketsSQL, id, tenantID, codes, subs, cities, zones); err != nil {
			return fmt.Errorf("hiringstore: save markets: %w", mapPgError(err))
		}
	}
	if _, err = tx.Exec(ctx, deleteSkillsSQL, id); err != nil {
		return fmt.Errorf("hiringstore: clear skills: %w", err)
	}
	if len(d.Skills) > 0 {
		ids, labels, pos := []string{}, []string{}, []int{}
		for i, sk := range d.Skills {
			ids, labels, pos = append(ids, sk.SkillID), append(labels, strings.TrimSpace(sk.CustomLabel)), append(pos, i)
		}
		if _, err = tx.Exec(ctx, insertSkillsSQL, id, tenantID, ids, labels, pos); err != nil {
			return fmt.Errorf("hiringstore: save skills: %w", mapPgError(err))
		}
	}
	return nil
}

func nonNilSections(s jobs.Sections) jobs.Sections {
	if s == nil {
		return jobs.Sections{}
	}
	return s
}

func writeForm(ctx context.Context, tx pgx.Tx, tenantID, actorID, id string, res *UpdateResult) error {
	var formID string
	if err := tx.QueryRow(ctx, upsertFormSQL, id, tenantID).Scan(&formID); err != nil {
		return fmt.Errorf("hiringstore: save form: %w", mapPgError(err))
	}
	existing, err := existingQuestionIDs(ctx, tx, formID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, deleteQuestionsSQL, formID); err != nil {
		return fmt.Errorf("hiringstore: clear questions: %w", err)
	}
	for i := range *res.Form {
		if err = insertQuestion(ctx, tx, tenantID, formID, i, &(*res.Form)[i], existing); err != nil {
			return err
		}
	}
	if res.Decls == nil {
		return nil
	}
	return writeDeclarations(ctx, tx, tenantID, actorID, id, res)
}

// existingQuestionIDs lists the ids of the questions already on a form, so a save keeps them.
func existingQuestionIDs(ctx context.Context, tx pgx.Tx, formID string) (map[string]bool, error) {
	existing := map[string]bool{}
	rows, err := tx.Query(ctx, existingQuestionIDsSQL, formID)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: list questions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var qid string
		if err = rows.Scan(&qid); err != nil {
			return nil, fmt.Errorf("hiringstore: scan question id: %w", err)
		}
		existing[strings.ToLower(qid)] = true
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("hiringstore: list questions: %w", err)
	}
	return existing, nil
}

// insertQuestion writes one question. It keeps the id of a question that already belongs to this form;
// anything else gets a new id, so an id copied from another job can never collide.
func insertQuestion(
	ctx context.Context, tx pgx.Tx, tenantID, formID string, position int, q *questions.FormQuestion,
	existing map[string]bool,
) error {
	qid := strings.ToLower(q.ID)
	if !existing[qid] {
		qid = ""
	}
	config := q.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	var knockout []byte // nil is SQL NULL
	if len(q.Knockout) > 0 && string(q.Knockout) != "null" {
		knockout = q.Knockout
	}
	if _, err := tx.Exec(ctx, insertQuestionSQL, qid, formID, tenantID, q.Section, position, q.Type, q.Label,
		q.Description, q.HelperText, q.Required, []byte(config), q.SystemKey, q.Locked, knockout,
		q.SpecialCategory); err != nil {
		return fmt.Errorf("hiringstore: save question %d: %w", position, mapPgError(err))
	}
	return nil
}

// writeDeclarations replaces the special category declarations of a job. They need a lawful basis, which is
// the one being saved with them or else the one already on the job.
func writeDeclarations(ctx context.Context, tx pgx.Tx, tenantID, actorID, id string, res *UpdateResult) error {
	if _, err := tx.Exec(ctx, deleteDeclarationsSQL, id); err != nil {
		return fmt.Errorf("hiringstore: clear declarations: %w", err)
	}
	if len(*res.Decls) == 0 {
		return nil
	}
	basis, retention := "", jobs.DefaultRetentionMonths
	if res.Details != nil {
		basis, retention = res.Details.LawfulBasis, res.Details.RetentionMonths
	} else if err := tx.QueryRow(ctx,
		`SELECT COALESCE(lawful_basis, ''), retention_months FROM hiring.job_postings WHERE id = $1::uuid`, id,
	).Scan(&basis, &retention); err != nil {
		return fmt.Errorf("hiringstore: load lawful basis: %w", err)
	}
	if basis == "" {
		return ErrLawfulBasisRequired
	}
	for _, d := range *res.Decls {
		if _, err := tx.Exec(ctx, insertDeclarationSQL, id, tenantID, d.Category, d.LegalCondition, d.Purpose,
			actorID, retention, basis); err != nil {
			return fmt.Errorf("hiringstore: save declaration: %w", mapPgError(err))
		}
	}
	return nil
}

// loadRecord reads a job, its markets, skills, form and declarations. forUpdate locks the job row.
func loadRecord(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (*JobRecord, error) {
	query := loadJobSQL
	if forUpdate {
		query += lockSuffix
	}
	var (
		rec         = &JobRecord{}
		j           = &rec.Job
		sections    []byte
		completed   []string
		payMin      *int64
		payMax      *int64
		payType     string
		payCurrency string
		payPeriod   string
	)
	err := tx.QueryRow(ctx, query, id, tenantID).Scan(
		&j.ID, &j.JobNumber, &j.CreatedBy, &j.Status, &j.CurrentStep, &completed, &j.Revision,
		&j.DepartmentName, &j.CreatedAt, &j.UpdatedAt, &j.PublishedAt,
		&j.Title, &j.DepartmentID, &j.ClosingDate, &j.LocationMode, &j.LocationText, &j.UseCompanyAddress,
		&j.WorkplaceType, &j.TravelFrequency, &j.VisaSponsorship, &sections, &j.IndustryID, &j.CustomIndustry,
		&j.EmploymentType, &j.SeniorityLevelID, &j.ExperienceRangeID,
		&payType, &payMin, &payMax, &payCurrency, &payPeriod, &j.Pay.Visible, &j.ShowOnCareerPage,
		&j.LawfulBasis, &j.LIAReference, &j.RetentionMonths, &j.AssessmentURL,
		&j.PublicID, &rec.DPIAConfirmedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("hiringstore: load job: %w", err)
	}
	j.JobCode = jobs.JobCode(j.JobNumber)
	j.CompletedSections = completed
	if j.CompletedSections == nil {
		j.CompletedSections = []string{}
	}
	j.Pay.Type, j.Pay.Currency, j.Pay.Period, j.Pay.Min, j.Pay.Max = payType, payCurrency, payPeriod, payMin, payMax
	j.Description = jobs.Sections{}
	if len(sections) > 0 {
		if err = json.Unmarshal(sections, &j.Description); err != nil {
			return nil, fmt.Errorf("hiringstore: decode description: %w", err)
		}
	}

	if j.Markets, err = loadMarkets(ctx, tx, id); err != nil {
		return nil, err
	}
	if j.Skills, err = loadSkills(ctx, tx, id); err != nil {
		return nil, err
	}
	if rec.Form, err = loadForm(ctx, tx, id); err != nil {
		return nil, err
	}
	if rec.Declarations, err = loadDeclarations(ctx, tx, id); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, loadFormRevisionSQL, id).Scan(&rec.FormRevision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("hiringstore: load form revision: %w", err)
	}
	if err = loadPhase2(ctx, tx, id, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func loadMarkets(ctx context.Context, tx pgx.Tx, id string) ([]jobs.MarketSel, error) {
	rows, err := tx.Query(ctx, loadMarketsSQL, id)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: load markets: %w", err)
	}
	defer rows.Close()
	out := []jobs.MarketSel{}
	for rows.Next() {
		var m jobs.MarketSel
		if err = rows.Scan(&m.MarketCode, &m.Subdivision, &m.City, &m.Timezone); err != nil {
			return nil, fmt.Errorf("hiringstore: scan market: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func loadSkills(ctx context.Context, tx pgx.Tx, id string) ([]jobs.Skill, error) {
	rows, err := tx.Query(ctx, loadSkillsSQL, id)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: load skills: %w", err)
	}
	defer rows.Close()
	out := []jobs.Skill{}
	for rows.Next() {
		var sk jobs.Skill
		if err = rows.Scan(&sk.SkillID, &sk.CustomLabel); err != nil {
			return nil, fmt.Errorf("hiringstore: scan skill: %w", err)
		}
		out = append(out, sk)
	}
	return out, rows.Err()
}

func loadForm(ctx context.Context, tx pgx.Tx, id string) ([]questions.FormQuestion, error) {
	rows, err := tx.Query(ctx, loadFormSQL, id)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: load form: %w", err)
	}
	defer rows.Close()
	out := []questions.FormQuestion{}
	for rows.Next() {
		var (
			q        questions.FormQuestion
			config   []byte
			knockout []byte
		)
		if err = rows.Scan(&q.ID, &q.Section, &q.Position, &q.Type, &q.Label, &q.Description, &q.HelperText,
			&q.Required, &config, &q.SystemKey, &q.Locked, &knockout, &q.SpecialCategory); err != nil {
			return nil, fmt.Errorf("hiringstore: scan question: %w", err)
		}
		q.Config = json.RawMessage(config)
		if len(knockout) > 0 {
			q.Knockout = json.RawMessage(knockout)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func loadDeclarations(ctx context.Context, tx pgx.Tx, id string) ([]questions.Declaration, error) {
	rows, err := tx.Query(ctx, loadDeclarationsSQL, id)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: load declarations: %w", err)
	}
	defer rows.Close()
	out := []questions.Declaration{}
	for rows.Next() {
		var d questions.Declaration
		if err = rows.Scan(&d.Category, &d.LegalCondition, &d.Purpose); err != nil {
			return nil, fmt.Errorf("hiringstore: scan declaration: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- list ----

// listJobsSQL is the jobs list query. The two %s are the sort column and the keyset direction, both picked from
// fixed lists in ListJobs, never from user text.
const listJobsSQL = `
SELECT j.id::text, j.job_number, j.status, COALESCE(j.title, ''), COALESCE(d.name, ''),
       COALESCE(j.employment_type, ''), COALESCE(j.location_mode, ''), j.current_step, j.revision,
       j.created_by::text,
       to_char(j.created_at AT TIME ZONE 'UTC', '` + tsLayout + `'),
       to_char(j.updated_at AT TIME ZONE 'UTC', '` + tsLayout + `'),
       COALESCE((SELECT array_agg(DISTINCT m.market_code ORDER BY m.market_code)
                 FROM hiring.job_markets m WHERE m.job_id = j.id), '{}'),
       COALESCE(j.workplace_type, ''),
       COALESCE(to_char(j.published_at AT TIME ZONE 'UTC', '` + tsLayout + `'), ''),
       COALESCE(to_char(j.closing_date, 'YYYY-MM-DD'), ''),
       COALESCE((SELECT array_agg(jm.user_id::text ORDER BY jm.user_id)
                 FROM hiring.job_members jm JOIN accounts.users u ON u.id = jm.user_id AND u.deleted_at IS NULL
                 WHERE jm.job_id = j.id AND jm.role = 'hiring_manager'), '{}'),
       COALESCE((SELECT array_agg(COALESCE(NULLIF(trim(concat_ws(' ', u.first_name, u.last_name)), ''),
                                           u.legal_full_name, '') ORDER BY jm.user_id)
                 FROM hiring.job_members jm JOIN accounts.users u ON u.id = jm.user_id AND u.deleted_at IS NULL
                 WHERE jm.job_id = j.id AND jm.role = 'hiring_manager'), '{}')
FROM hiring.job_postings j
LEFT JOIN hiring.departments d ON d.id = j.department_id
WHERE j.tenant_id = $1::uuid AND j.deleted_at IS NULL
  AND ($2::text[] IS NULL OR j.status = ANY($2::text[]))
  AND ($3::uuid IS NULL OR j.department_id = $3::uuid)
  AND ($4::text IS NULL OR j.title ILIKE '%%' || $4::text || '%%' ESCAPE '\'
       OR d.name ILIKE '%%' || $4::text || '%%' ESCAPE '\')
  AND ($5::uuid IS NULL OR j.created_by = $5::uuid
       OR EXISTS (SELECT 1 FROM hiring.job_members jm WHERE jm.job_id = j.id AND jm.user_id = $5::uuid))
  AND ($6::timestamptz IS NULL OR (j.%[1]s, j.id) %[2]s ($6::timestamptz, $7::uuid))
  AND ($9::uuid IS NULL OR j.created_by = $9::uuid)
  AND ($10::uuid IS NULL OR EXISTS (SELECT 1 FROM hiring.job_members jm
                                    WHERE jm.job_id = j.id AND jm.user_id = $10::uuid))
  AND ($11::text IS NULL OR j.employment_type = $11::text)
  AND ($12::text IS NULL OR j.workplace_type = $12::text)
  AND ($13::text IS NULL OR j.location_mode = $13::text)
  AND ($14::text IS NULL OR EXISTS (SELECT 1 FROM hiring.job_markets m
                                    WHERE m.job_id = j.id AND m.market_code = $14::text))
  AND ($15::date IS NULL OR (j.created_at AT TIME ZONE 'UTC')::date >= $15::date)
  AND ($16::date IS NULL OR (j.created_at AT TIME ZONE 'UTC')::date <= $16::date)
ORDER BY j.%[1]s %[3]s, j.id %[3]s
LIMIT $8`

// listSQL fills in the sort column and direction. Anything not on the fixed lists falls back to newest updated.
func listSQL(sortKey string, ascending bool) string {
	col := "updated_at"
	if sortKey == SortCreated {
		col = "created_at"
	}
	cmp, dir := "<", "DESC"
	if ascending {
		cmp, dir = ">", "ASC"
	}
	return fmt.Sprintf(listJobsSQL, col, cmp, dir)
}

// SortCreated and SortUpdated mirror the sort keys of hiringtypes so callers in this package read plainly.
const (
	SortCreated = hiringtypes.SortCreated
	SortUpdated = hiringtypes.SortUpdated
)

// EscapeLike escapes the LIKE wildcards in user text so a search for "100%" matches literally.
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// optText returns a pointer to s, or nil when s is empty, so an empty filter reaches the query as NULL.
func optText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListJobs returns one page and the cursor of the next page ("" at the end). Newest first unless the filter says
// otherwise.
func (s *Store) ListJobs(ctx context.Context, tenantID string, f *ListFilter) ([]ListItem, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	var (
		statuses []string
		dept     *string
		q        *string
		curTS    *string
		curID    *string
	)
	if len(f.Statuses) > 0 {
		statuses = f.Statuses
	}
	dept = optText(f.DepartmentID)
	if t := strings.TrimSpace(f.Query); t != "" {
		esc := EscapeLike(t)
		q = &esc
	}
	if f.Cursor != "" {
		ts, id, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		curTS, curID = &ts, &id
	}
	query := listSQL(f.Sort, f.Ascending)

	var items []ListItem
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// One extra row tells us whether another page exists.
		rows, err := tx.Query(ctx, query, tenantID, statuses, dept, q, optText(f.OnlyMine), curTS, curID, limit+1,
			optText(f.CreatedBy), optText(f.Assignee), optText(f.EmploymentType), optText(f.WorkplaceType),
			optText(f.LocationMode), optText(f.Market), optText(f.CreatedFrom), optText(f.CreatedTo))
		if err != nil {
			return fmt.Errorf("hiringstore: list jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var it ListItem
			var number int
			var managerIDs, managerNames []string
			if err = rows.Scan(&it.ID, &number, &it.Status, &it.Title, &it.DepartmentName, &it.EmploymentType,
				&it.LocationMode, &it.CurrentStep, &it.Revision, &it.CreatedBy, &it.CreatedAt, &it.UpdatedAt,
				&it.Markets, &it.WorkplaceType, &it.PublishedAt, &it.ClosingDate,
				&managerIDs, &managerNames); err != nil {
				return fmt.Errorf("hiringstore: scan job: %w", err)
			}
			it.JobCode = jobs.JobCode(number)
			it.Managers = make([]ListPerson, 0, len(managerIDs))
			for i, id := range managerIDs {
				name := ""
				if i < len(managerNames) {
					name = managerNames[i]
				}
				it.Managers = append(it.Managers, ListPerson{UserID: id, Name: name})
			}
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		stamp := last.UpdatedAt
		if f.Sort == SortCreated {
			stamp = last.CreatedAt
		}
		next = EncodeCursor(stamp, last.ID)
	}
	if items == nil {
		items = []ListItem{}
	}
	return items, next, nil
}

// ---- delete, duplicates, audit ----

const softDeleteSQL = `
UPDATE hiring.job_postings SET deleted_at = now(), revision = revision + 1
WHERE id = $1::uuid AND tenant_id = $2::uuid AND deleted_at IS NULL AND status = 'draft'
RETURNING revision`

// DeleteDraft soft-deletes a draft. Jobs in any other status cannot be deleted here.
func (s *Store) DeleteDraft(ctx context.Context, tenantID, actorID, id string, ifMatch int) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := loadRecord(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if ifMatch != 0 && cur.Job.Revision != ifMatch {
			return &StaleRevisionError{Current: cur.Job.Revision}
		}
		var revision int
		if err = tx.QueryRow(ctx, softDeleteSQL, id, tenantID).Scan(&revision); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotDraft
		} else if err != nil {
			return fmt.Errorf("hiringstore: delete draft: %w", err)
		}
		_, err = tx.Exec(ctx, insertAuditSQL, id, tenantID, actorID, "job.deleted", []byte(`{}`))
		return err
	})
}

const duplicatesSQL = `
SELECT j.id::text, j.job_number, COALESCE(j.title, ''), j.status
FROM hiring.job_postings j
WHERE j.tenant_id = $1::uuid AND j.deleted_at IS NULL AND j.id <> $2::uuid
  AND $3::text <> '' AND j.title_key = $3::text AND j.location_key = $4::text
  AND (j.status IN ('draft', 'pending_approval', 'published', 'paused')
       OR (j.status = 'closed' AND j.closed_at > now() - make_interval(days => $5)))
  AND ($6::uuid IS NULL OR j.created_by = $6::uuid
       OR EXISTS (SELECT 1 FROM hiring.job_members jm WHERE jm.job_id = j.id AND jm.user_id = $6::uuid))
ORDER BY j.updated_at DESC
LIMIT 5`

// FindDuplicates returns the jobs that share a job's normalised title and locations. onlyMine limits the
// result to jobs the caller can see, so the warning never reveals a job the user cannot open.
func (s *Store) FindDuplicates(
	ctx context.Context, tenantID, jobID, titleKey, locationKey, onlyMine string,
) ([]jobs.Duplicate, error) {
	var mine *string
	if onlyMine != "" {
		mine = &onlyMine
	}
	out := []jobs.Duplicate{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, duplicatesSQL, tenantID, jobID, titleKey, locationKey, jobs.DuplicateWindowDays, mine)
		if err != nil {
			return fmt.Errorf("hiringstore: find duplicates: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d jobs.Duplicate
			var number int
			if err = rows.Scan(&d.ID, &number, &d.Title, &d.Status); err != nil {
				return fmt.Errorf("hiringstore: scan duplicate: %w", err)
			}
			d.JobCode = jobs.JobCode(number)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// RecordAudit writes one audit event for a job without changing it.
func (s *Store) RecordAudit(ctx context.Context, tenantID, actorID, jobID, action string, diff map[string]any) error {
	raw, err := json.Marshal(nonNil(diff))
	if err != nil {
		return fmt.Errorf("hiringstore: encode audit diff: %w", err)
	}
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err = loadRecord(ctx, tx, tenantID, jobID, false); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, insertAuditSQL, jobID, tenantID, actorID, action, raw)
		return err
	})
}
