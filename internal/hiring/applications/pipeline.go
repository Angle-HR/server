package applications

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/rbac"
)

// The pipeline: a job's stages, and what the hiring team keeps on an application (notes, tags, follows) with the
// activity timeline. Status stays the coarse lifecycle of status.go's rules; a stage is where the candidate is
// inside it. Hired, Disqualified and Withdrawn are final: nothing moves a candidate out of them.

// Stage kinds. The kind is stable across jobs; the name is the company's wording.
const (
	KindApplied      = "applied"
	KindShortlisted  = "shortlisted"
	KindInterview    = "interview"
	KindOffer        = "offer"
	KindHired        = "hired"
	KindDisqualified = "disqualified"
	KindCustom       = "custom"
)

// Limits.
const (
	MaxStages          = 20
	MaxStageName       = 40
	MaxNoteBody        = 4000
	MaxTagName         = 30
	MaxTagsPerApp      = 10
	MaxBulk            = 100
	DefaultActivityLen = 30
	MaxActivityLen     = 100
)

// Stage is one step of a job's pipeline.
type Stage struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Position int    `json:"position"`
	// Count is how many applications are in the stage now.
	Count int `json:"count"`
	// Final stages (Hired, Disqualified) end the application.
	Final bool `json:"final"`
}

// FinalKind reports whether a stage kind ends the application.
func FinalKind(kind string) bool { return kind == KindHired || kind == KindDisqualified }

// Reason is a disqualification reason.
type Reason struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// DisqualifyReasons is the catalog a company picks from when it disqualifies a candidate.
var DisqualifyReasons = []Reason{
	{"not_a_fit", "Not a fit for the role"},
	{"lacks_experience", "Lacks the required experience"},
	{"lacks_skills", "Lacks the required skills"},
	{"salary_expectations", "Salary expectations too high"},
	{"location", "Location or right to work"},
	{"failed_screening", "Did not pass screening"},
	{"no_response", "Stopped responding"},
	{"position_filled", "Position filled"},
	{"other", "Other"},
}

func validReason(code string) bool {
	for _, r := range DisqualifyReasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

// Note is an internal note on an application. Candidates never see notes.
type Note struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"author_id,omitempty"`
	Body      string    `json:"body"`
	Mine      bool      `json:"mine"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Tag is a company label that can be put on applications.
type Tag struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// TagColors are the colors a tag can have.
var TagColors = []string{"gray", "red", "orange", "yellow", "green", "teal", "blue", "purple", "pink"}

// Activity event types.
const (
	EventApplied       = "applied"
	EventStageChanged  = "stage_changed"
	EventStatusChanged = "status_changed"
	EventNoteAdded     = "note_added"
	EventNoteEdited    = "note_edited"
	EventNoteDeleted   = "note_deleted"
	EventTagsChanged   = "tags_changed"
	// Candidate email: written, sent (by the sender, with no actor) and cancelled.
	EventMessage          = "message_queued"
	EventMessageSent      = "message_sent"
	EventMessageCancelled = "message_cancelled"
)

// Event is one entry of an application's timeline. Data holds ids and short labels, never note text.
type Event struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	ActorID   string          `json:"actor_id,omitempty"`
	ActorName string          `json:"actor_name,omitempty"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// ActivityPage is one page of the timeline, newest first.
type ActivityPage struct {
	Items      []Event `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

// Errors of the pipeline.
var (
	// ErrStageLocked: the stage is one of the fixed ones and cannot be renamed or removed.
	ErrStageLocked = errors.New("applications: this stage cannot be changed")
	// ErrStageInUse: the stage still has applications in it.
	ErrStageInUse = errors.New("applications: move the applications out of this stage first")
	// ErrTooManyStages: a job has at most MaxStages stages.
	ErrTooManyStages = errors.New("applications: too many stages")
	// ErrInvalidOrder: the order does not list exactly the job's open stages with Applied first.
	ErrInvalidOrder = errors.New("applications: the order must list every open stage once, with Applied first")
	// ErrExists: a stage or tag with that name already exists.
	ErrExists = errors.New("applications: that name is already used")
	// ErrUnknownTag: a tag id is not one of the company's tags.
	ErrUnknownTag = errors.New("applications: unknown tag")
	// ErrNoPipeline: the pipeline store is not configured.
	ErrNoPipeline = errors.New("applications: the pipeline is not available")
)

// MoveState is what a stage move needs to decide, read before the change is made.
type MoveState struct {
	Status string
	// StageID is the application's current stage, "" when it has none.
	StageID string
	Target  Stage
}

// OpenMove moves an application to an open stage in one transaction. When ToStatus differs from FromStatus the
// application also goes from received to in_review.
type OpenMove struct {
	AppID, JobID, ActorID string
	FromStatus, ToStatus  string
	FromStageID           string
	To                    Stage
	Now                   time.Time
}

// PipelineStore is one region's database as the pipeline uses it.
type PipelineStore interface {
	// Stages lists the job's stages with their counts, creating the default ones the first time.
	Stages(ctx context.Context, tenantID, jobID string) ([]Stage, error)
	CreateStage(ctx context.Context, tenantID, jobID, actorID, name string) (*Stage, error)
	RenameStage(ctx context.Context, tenantID, jobID, stageID, actorID, name string) error
	DeleteStage(ctx context.Context, tenantID, jobID, stageID, actorID string) error
	ReorderStages(ctx context.Context, tenantID, jobID, actorID string, openStageIDs []string) error

	MoveState(ctx context.Context, tenantID, jobID, appID, stageID string) (*MoveState, error)
	MoveToOpenStage(ctx context.Context, tenantID string, m *OpenMove) error

	Notes(ctx context.Context, tenantID, jobID, appID, viewerID string) ([]Note, error)
	AddNote(ctx context.Context, tenantID, jobID, appID, authorID, body string) (*Note, error)
	EditNote(ctx context.Context, tenantID, jobID, appID, noteID, authorID, body string) (*Note, error)
	DeleteNote(ctx context.Context, tenantID, jobID, appID, noteID, authorID string) error

	Tags(ctx context.Context, tenantID string) ([]Tag, error)
	CreateTag(ctx context.Context, tenantID, actorID, name, color string) (*Tag, error)
	DeleteTag(ctx context.Context, tenantID, tagID string) error
	SetTags(ctx context.Context, tenantID, jobID, appID, actorID string, tagIDs []string) ([]Tag, error)

	Follow(ctx context.Context, tenantID, jobID, appID, userID string, on bool) error
	Activity(ctx context.Context, tenantID, jobID, appID string, beforeID int64, limit int) ([]Event, error)
}

// UserNames resolves display names of users for the timeline and the notes. Optional: without it the actor is an
// id only.
type UserNames interface {
	Names(ctx context.Context, region string, userIDs []string) (map[string]string, error)
}

func (s *Service) pipeline() (PipelineStore, error) {
	if s.Pipeline == nil {
		return nil, ErrNoPipeline
	}
	return s.Pipeline, nil
}

func cleanName(name string, max int, field string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", invalid(field, "enter a name")
	}
	if utf8.RuneCountInString(name) > max {
		return "", invalid(field, fmt.Sprintf("keep the name under %d characters", max+1))
	}
	return name, nil
}

// stageJob checks access to the job for a stage change: the people who decide on its applications.
func (s *Service) deciderOn(ctx context.Context, c Caller, jobID string) (*JobAccess, PipelineStore, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, nil, err
	}
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, nil, err
	}
	if !canDecide(c, a) {
		return nil, nil, forbidden("you cannot change the pipeline of this job")
	}
	return a, p, nil
}

// Stages lists the job's pipeline with the number of applications in each stage.
func (s *Service) Stages(ctx context.Context, c Caller, jobID string) ([]Stage, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	return p.Stages(ctx, c.OrgID, jobID)
}

// CreateStage adds a custom stage before Hired.
func (s *Service) CreateStage(ctx context.Context, c Caller, jobID, name string) (*Stage, error) {
	_, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if name, err = cleanName(name, MaxStageName, "name"); err != nil {
		return nil, err
	}
	return p.CreateStage(ctx, c.OrgID, jobID, c.UserID, name)
}

// RenameStage renames a stage. Applied, Hired and Disqualified keep their names.
func (s *Service) RenameStage(ctx context.Context, c Caller, jobID, stageID, name string) error {
	_, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return err
	}
	if !formschema.IsUUID(stageID) {
		return ErrNotFound
	}
	if name, err = cleanName(name, MaxStageName, "name"); err != nil {
		return err
	}
	return p.RenameStage(ctx, c.OrgID, jobID, stageID, c.UserID, name)
}

// DeleteStage removes an empty custom stage.
func (s *Service) DeleteStage(ctx context.Context, c Caller, jobID, stageID string) error {
	_, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return err
	}
	if !formschema.IsUUID(stageID) {
		return ErrNotFound
	}
	return p.DeleteStage(ctx, c.OrgID, jobID, stageID, c.UserID)
}

// ReorderStages sets the order of the open stages. Hired and Disqualified stay last.
func (s *Service) ReorderStages(ctx context.Context, c Caller, jobID string, ids []string) error {
	_, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !formschema.IsUUID(id) || seen[id] {
			return ErrInvalidOrder
		}
		seen[id] = true
	}
	return p.ReorderStages(ctx, c.OrgID, jobID, c.UserID, ids)
}

// MoveInput moves an application to a stage.
type MoveInput struct {
	StageID string
	// Reason is a DisqualifyReasons code. Needed for the Disqualified stage.
	Reason string
	Note   string
	// Message is emailed to the candidate when the move decides the application (Hired or Disqualified).
	Message *MessageInput
}

// MoveResult is the outcome of a stage move.
type MoveResult struct {
	Status    string     `json:"status"`
	StageID   string     `json:"stage_id"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

// MoveStage moves an application to another stage of its job.
//
//   - To an open stage (Applied, Shortlisted, Interview, Offer, custom): a received application becomes in_review
//     unless the target is Applied.
//   - To Hired or Disqualified: the application is decided, as the status endpoint decides it, except that it may
//     go straight from received. Disqualified needs a reason. The candidate is emailed as for any decision.
//   - A flagged application is moved only after its review, a decided one never.
func (s *Service) MoveStage(ctx context.Context, c Caller, jobID, appID string, in MoveInput) (*MoveResult, error) {
	a, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) || !formschema.IsUUID(in.StageID) {
		return nil, ErrNotFound
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return nil, err
	}
	ms, err := p.MoveState(ctx, c.OrgID, jobID, appID, in.StageID)
	if err != nil {
		return nil, err
	}
	if Final(ms.Status) {
		return nil, ErrInvalidTransition
	}
	if ms.StageID == ms.Target.ID {
		return &MoveResult{Status: ms.Status, StageID: ms.StageID}, nil
	}
	if ms.Status == StatusFlagged {
		return nil, ErrReviewRequired
	}
	if FinalKind(ms.Target.Kind) {
		to, reason := StatusHired, ""
		if ms.Target.Kind == KindDisqualified {
			to, reason = StatusRejected, in.Reason
			if !validReason(reason) {
				return nil, invalid("reason", "choose a reason for disqualifying the candidate")
			}
		}
		st, err := s.loadChange(ctx, c, jobID, appID)
		if err != nil {
			return nil, err
		}
		res, err := s.apply(ctx, c, a, st, appID, to, note, "", applyOpts{StageID: ms.Target.ID, Reason: reason, Message: in.Message})
		if err != nil {
			return nil, err
		}
		return &MoveResult{Status: res.Status, StageID: ms.Target.ID, DecidedAt: res.DecidedAt}, nil
	}
	if in.Message != nil {
		return nil, invalid("message", "a message goes with a hire or a disqualification")
	}
	to := ms.Status
	if ms.Status == StatusReceived && ms.Target.Kind != KindApplied {
		to = StatusInReview
	}
	now := s.now().UTC()
	err = p.MoveToOpenStage(ctx, c.OrgID, &OpenMove{
		AppID: appID, JobID: jobID, ActorID: c.UserID, FromStatus: ms.Status, ToStatus: to,
		FromStageID: ms.StageID, To: ms.Target, Now: now,
	})
	if err != nil {
		return nil, err
	}
	return &MoveResult{Status: to, StageID: ms.Target.ID}, nil
}

// BulkItem is the outcome for one application of a bulk move.
type BulkItem struct {
	ApplicationID string `json:"application_id"`
	OK            bool   `json:"ok"`
	// Reason says why it was left where it is: not_found, flagged (review it first), final, invalid or changed.
	Reason string `json:"reason,omitempty"`
}

// BulkMoveStage moves several applications to one stage. Each is its own change: one that cannot move is reported
// and the others still do. A flagged application is never moved in bulk.
func (s *Service) BulkMoveStage(ctx context.Context, c Caller, jobID string, ids []string, in MoveInput) ([]BulkItem, error) {
	if len(ids) == 0 || len(ids) > MaxBulk {
		return nil, invalid("application_ids", fmt.Sprintf("choose between 1 and %d applications", MaxBulk))
	}
	_, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	// Check the request once, so a missing reason fails the whole call instead of every item.
	if !formschema.IsUUID(in.StageID) {
		return nil, ErrNotFound
	}
	stages, err := p.Stages(ctx, c.OrgID, jobID)
	if err != nil {
		return nil, err
	}
	var target *Stage
	for i := range stages {
		if stages[i].ID == in.StageID {
			target = &stages[i]
		}
	}
	if target == nil {
		return nil, ErrNotFound
	}
	if target.Kind == KindDisqualified && !validReason(in.Reason) {
		return nil, invalid("reason", "choose a reason for disqualifying the candidates")
	}
	out := make([]BulkItem, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		item := BulkItem{ApplicationID: id}
		_, err := s.MoveStage(ctx, c, jobID, id, in)
		switch {
		case err == nil:
			item.OK = true
		case errors.Is(err, ErrNotFound):
			item.Reason = "not_found"
		case errors.Is(err, ErrReviewRequired):
			item.Reason = "flagged"
		case errors.Is(err, ErrInvalidTransition):
			item.Reason = "final"
		case errors.Is(err, ErrStale):
			item.Reason = "changed"
		default:
			item.Reason = "error"
		}
		out = append(out, item)
	}
	return out, nil
}

// ---- notes ----

// Notes lists an application's notes, newest first. Anyone who can see the job reads them.
func (s *Service) Notes(ctx context.Context, c Caller, jobID, appID string) ([]Note, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	return p.Notes(ctx, c.OrgID, jobID, appID, c.UserID)
}

func cleanBody(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", invalid("body", "write something")
	}
	if utf8.RuneCountInString(body) > MaxNoteBody {
		return "", invalid("body", fmt.Sprintf("a note can be at most %d characters", MaxNoteBody))
	}
	return body, nil
}

// AddNote writes a note. Anyone who can see the job can: interviewers leave their feedback this way.
func (s *Service) AddNote(ctx context.Context, c Caller, jobID, appID, body string) (*Note, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	if body, err = cleanBody(body); err != nil {
		return nil, err
	}
	return p.AddNote(ctx, c.OrgID, jobID, appID, c.UserID, body)
}

// EditNote changes a note. Only its author can.
func (s *Service) EditNote(ctx context.Context, c Caller, jobID, appID, noteID, body string) (*Note, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) || !formschema.IsUUID(noteID) {
		return nil, ErrNotFound
	}
	if body, err = cleanBody(body); err != nil {
		return nil, err
	}
	return p.EditNote(ctx, c.OrgID, jobID, appID, noteID, c.UserID, body)
}

// DeleteNote removes a note. Only its author can.
func (s *Service) DeleteNote(ctx context.Context, c Caller, jobID, appID, noteID string) error {
	p, err := s.pipeline()
	if err != nil {
		return err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return err
	}
	if !formschema.IsUUID(appID) || !formschema.IsUUID(noteID) {
		return ErrNotFound
	}
	return p.DeleteNote(ctx, c.OrgID, jobID, appID, noteID, c.UserID)
}

// ---- tags ----

func canSeeAnyJob(c Caller) bool { return has(c, rbac.JobViewAll) || has(c, rbac.JobViewAssigned) }

// Tags lists the company's tags.
func (s *Service) Tags(ctx context.Context, c Caller) ([]Tag, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if !canSeeAnyJob(c) {
		return nil, forbidden("you cannot see tags")
	}
	return p.Tags(ctx, c.OrgID)
}

// CreateTag adds a tag to the company's catalog. It needs job.manage_any.
func (s *Service) CreateTag(ctx context.Context, c Caller, name, color string) (*Tag, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if !has(c, rbac.JobManageAny) {
		return nil, forbidden("you cannot manage the company's tags")
	}
	if name, err = cleanName(name, MaxTagName, "name"); err != nil {
		return nil, err
	}
	if color == "" {
		color = "gray"
	}
	okColor := false
	for _, tc := range TagColors {
		okColor = okColor || tc == color
	}
	if !okColor {
		return nil, invalid("color", "choose one of "+strings.Join(TagColors, ", "))
	}
	return p.CreateTag(ctx, c.OrgID, c.UserID, name, color)
}

// DeleteTag removes a tag from the catalog and from every application. It needs job.manage_any.
func (s *Service) DeleteTag(ctx context.Context, c Caller, tagID string) error {
	p, err := s.pipeline()
	if err != nil {
		return err
	}
	if !has(c, rbac.JobManageAny) {
		return forbidden("you cannot manage the company's tags")
	}
	if !formschema.IsUUID(tagID) {
		return ErrNotFound
	}
	return p.DeleteTag(ctx, c.OrgID, tagID)
}

// SetTags replaces the tags on an application. It returns the application's tags.
func (s *Service) SetTags(ctx context.Context, c Caller, jobID, appID string, ids []string) ([]Tag, error) {
	_, p, err := s.deciderOn(ctx, c, jobID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	if len(ids) > MaxTagsPerApp {
		return nil, invalid("tag_ids", fmt.Sprintf("an application can have at most %d tags", MaxTagsPerApp))
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		if !formschema.IsUUID(id) {
			return nil, invalid("tag_ids", "unknown tag")
		}
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	return p.SetTags(ctx, c.OrgID, jobID, appID, c.UserID, uniq)
}

// ---- follow ----

// Follow stars or unstars an application for the caller. Anyone who can see the job can.
func (s *Service) Follow(ctx context.Context, c Caller, jobID, appID string, on bool) error {
	p, err := s.pipeline()
	if err != nil {
		return err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return err
	}
	if !formschema.IsUUID(appID) {
		return ErrNotFound
	}
	return p.Follow(ctx, c.OrgID, jobID, appID, c.UserID, on)
}

// ---- activity ----

// Activity returns an application's timeline, newest first. cursor is the next_cursor of the previous page.
func (s *Service) Activity(ctx context.Context, c Caller, jobID, appID, cursor string, limit int) (*ActivityPage, error) {
	p, err := s.pipeline()
	if err != nil {
		return nil, err
	}
	if _, err = s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, ErrNotFound
	}
	switch {
	case limit <= 0:
		limit = DefaultActivityLen
	case limit > MaxActivityLen:
		limit = MaxActivityLen
	}
	var before int64
	if cursor != "" {
		raw, derr := base64.RawURLEncoding.DecodeString(cursor)
		if derr == nil {
			before, derr = strconv.ParseInt(string(raw), 10, 64)
		}
		if derr != nil || before < 1 {
			return nil, invalid("cursor", "invalid cursor")
		}
	}
	items, err := p.Activity(ctx, c.OrgID, jobID, appID, before, limit+1)
	if err != nil {
		return nil, err
	}
	page := &ActivityPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(page.Items[limit-1].ID, 10)))
	}
	if page.Items == nil {
		page.Items = []Event{}
	}
	s.nameActors(ctx, c, page.Items)
	return page, nil
}

// nameActors fills the actors' display names when a directory is configured. A directory outage only drops names.
func (s *Service) nameActors(ctx context.Context, c Caller, items []Event) {
	if s.Names == nil {
		return
	}
	var ids []string
	seen := map[string]bool{}
	for _, e := range items {
		if e.ActorID != "" && !seen[e.ActorID] {
			seen[e.ActorID] = true
			ids = append(ids, e.ActorID)
		}
	}
	if len(ids) == 0 {
		return
	}
	names, err := s.Names.Names(ctx, c.Region, ids)
	if err != nil {
		return
	}
	for i := range items {
		items[i].ActorName = names[items[i].ActorID]
	}
}
