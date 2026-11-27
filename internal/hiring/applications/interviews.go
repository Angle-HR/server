package applications

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/ical"
	"github.com/Angle-HR/server/pkg/formschema"
	"github.com/Angle-HR/server/pkg/rbac"
)

// Limits of interviews.
const (
	MaxInterviewers     = 10
	MaxActiveInterviews = 10 // scheduled interviews of one application
	MaxLocation         = 300
	MaxInterviewNotes   = 2000
	MaxInviteNote       = 1000
	DefaultDuration     = 60
	MinDuration         = 15
	MaxDuration         = 480
	MaxScheduleAhead    = 366 * 24 * time.Hour
	MaxScheduleList     = 200
)

// Interview kinds and statuses.
const (
	KindVideo  = "video"
	KindPhone  = "phone"
	KindOnsite = "onsite"

	InterviewScheduled = "scheduled"
	InterviewCompleted = "completed"
	InterviewCancelled = "cancelled"
)

// Timeline events of interviews.
const (
	EventInterviewScheduled   = "interview_scheduled"
	EventInterviewRescheduled = "interview_updated"
	EventInterviewCancelled   = "interview_cancelled"
	EventInterviewCompleted   = "interview_completed"
)

// ErrTooManyInterviews: the application has the most scheduled interviews it can have.
var ErrTooManyInterviews = errors.New("applications: too many scheduled interviews")

// ErrNoInterviews: the service has no interview store.
var ErrNoInterviews = errors.New("applications: interviews are not available")

// ErrInterviewClosed: the interview is completed or cancelled and cannot change.
var ErrInterviewClosed = errors.New("applications: interview is closed")

// Interview is a conversation with a candidate.
type Interview struct {
	ID             string    `json:"id"`
	JobID          string    `json:"job_id"`
	ApplicationID  string    `json:"application_id"`
	StartsAt       time.Time `json:"starts_at"`
	EndsAt         time.Time `json:"ends_at"`
	DurationMin    int       `json:"duration_minutes"`
	Timezone       string    `json:"timezone"`
	Kind           string    `json:"kind"`
	Location       string    `json:"location"`
	Notes          string    `json:"notes"`
	InterviewerIDs []string  `json:"interviewer_ids"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	// Sequence counts changes; it is the calendar event's SEQUENCE.
	Sequence int `json:"-"`
	// Set on a person's schedule: who the interview is with and for which job.
	CandidateName string `json:"candidate_name,omitempty"`
	JobCode       string `json:"job_code,omitempty"`
	JobTitle      string `json:"job_title,omitempty"`
}

// InterviewStore keeps interviews. Writes that send an invite save the message in the same transaction.
type InterviewStore interface {
	Interviews(ctx context.Context, tenantID, jobID, appID string) ([]Interview, error)
	Interview(ctx context.Context, tenantID, jobID, appID, id string) (*Interview, error)
	// CreateInterview saves iv (its ID is set) and the invite message, if any.
	CreateInterview(ctx context.Context, tenantID, actorID string, iv *Interview, msg *NewMessage) error
	// SaveInterview replaces an interview that is still at prevSeq (ErrStale otherwise), with the message, if any.
	// event is the timeline entry.
	SaveInterview(ctx context.Context, tenantID, actorID string, iv *Interview, prevSeq int, event string, msg *NewMessage) error
	// MyInterviews lists scheduled interviews in [from, to) where the user is an interviewer, soonest first.
	MyInterviews(ctx context.Context, tenantID, userID string, from, to time.Time, limit int) ([]Interview, error)
}

func (s *Service) interviews() (InterviewStore, error) {
	if s.Schedule == nil {
		return nil, ErrNoInterviews
	}
	return s.Schedule, nil
}

// InterviewInput schedules an interview.
type InterviewInput struct {
	StartsAt       time.Time
	DurationMin    int // 0 means 60
	Timezone       string
	Kind           string
	Location       string
	Notes          string
	InterviewerIDs []string
	// Invite emails the candidate a calendar invite. Needs the company's reply-to address, where replies go.
	Invite     bool
	InviteNote string
}

// InterviewPatch changes an interview; nil leaves a field as it is.
type InterviewPatch struct {
	StartsAt       *time.Time
	DurationMin    *int
	Timezone       *string
	Kind           *string
	Location       *string
	Notes          *string
	InterviewerIDs *[]string
	// Invite emails the candidate the updated invite.
	Invite     bool
	InviteNote string
}

func cleanLocation(kind, loc string) (string, error) {
	loc = strings.TrimSpace(loc)
	if utf8.RuneCountInString(loc) > MaxLocation {
		return "", invalid("location", fmt.Sprintf("keep this under %d characters", MaxLocation+1))
	}
	switch kind {
	case KindVideo:
		u, err := url.Parse(loc)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || strings.ContainsAny(loc, " \r\n") {
			return "", invalid("location", "enter the meeting link, starting with https://")
		}
	case KindOnsite:
		if loc == "" {
			return "", invalid("location", "enter the address")
		}
	}
	if strings.ContainsAny(loc, "\r\n") {
		return "", invalid("location", "keep this on one line")
	}
	return loc, nil
}

// validate checks an interview's fields and fills the derived ones.
func (s *Service) validateInterview(ctx context.Context, c Caller, a *JobAccess, iv *Interview, checkTime bool) error {
	if iv.DurationMin == 0 {
		iv.DurationMin = DefaultDuration
	}
	if iv.DurationMin < MinDuration || iv.DurationMin > MaxDuration {
		return invalid("duration_minutes", fmt.Sprintf("choose between %d and %d minutes", MinDuration, MaxDuration))
	}
	if _, err := time.LoadLocation(iv.Timezone); err != nil || iv.Timezone == "" || iv.Timezone == "Local" {
		return invalid("timezone", "enter a time zone such as Europe/London")
	}
	if !contains([]string{KindVideo, KindPhone, KindOnsite}, iv.Kind) {
		return invalid("kind", "kind must be video, phone or onsite")
	}
	loc, err := cleanLocation(iv.Kind, iv.Location)
	if err != nil {
		return err
	}
	iv.Location = loc
	iv.Notes = strings.TrimSpace(iv.Notes)
	if utf8.RuneCountInString(iv.Notes) > MaxInterviewNotes {
		return invalid("notes", fmt.Sprintf("keep this under %d characters", MaxInterviewNotes+1))
	}
	now := s.now()
	if iv.StartsAt.IsZero() || (checkTime && (!iv.StartsAt.After(now) || iv.StartsAt.After(now.Add(MaxScheduleAhead)))) {
		return invalid("starts_at", "choose a time in the future, within a year")
	}
	iv.StartsAt = iv.StartsAt.UTC()
	iv.EndsAt = iv.StartsAt.Add(time.Duration(iv.DurationMin) * time.Minute)
	if len(iv.InterviewerIDs) > MaxInterviewers {
		return invalid("interviewer_ids", fmt.Sprintf("choose at most %d interviewers", MaxInterviewers))
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range iv.InterviewerIDs {
		if !formschema.IsUUID(id) {
			return invalid("interviewer_ids", "unknown interviewer")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		// An interviewer is someone who can see the job: its creator or a member of its team.
		ia, err := s.Jobs.JobAccess(ctx, c.OrgID, a.JobID, id)
		if err != nil || (ia.MemberRole == "" && ia.CreatedBy != id) {
			return invalid("interviewer_ids", "interviewers must be on the job's team")
		}
	}
	iv.InterviewerIDs = ids
	return nil
}

func (s *Service) interviewTarget(ctx context.Context, c Caller, jobID, appID string) (*JobAccess, InterviewStore, error) {
	is, err := s.interviews()
	if err != nil {
		return nil, nil, err
	}
	a, err := s.access(ctx, c, jobID)
	if err != nil {
		return nil, nil, err
	}
	if !formschema.IsUUID(appID) {
		return nil, nil, ErrNotFound
	}
	return a, is, nil
}

func (s *Service) interviewDecider(ctx context.Context, c Caller, jobID, appID string) (*JobAccess, InterviewStore, error) {
	a, is, err := s.interviewTarget(ctx, c, jobID, appID)
	if err != nil {
		return nil, nil, err
	}
	if !canDecide(c, a) {
		return nil, nil, forbidden("you cannot schedule interviews on this job")
	}
	return a, is, nil
}

// Interviews lists an application's interviews, soonest first. Anyone who can see the job can.
func (s *Service) Interviews(ctx context.Context, c Caller, jobID, appID string) ([]Interview, error) {
	_, is, err := s.interviewTarget(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	return is.Interviews(ctx, c.OrgID, jobID, appID)
}

// inviteMessage writes the email and the calendar event of an invite ("invite"), a change ("update") or a
// cancellation ("cancel"). It needs the company's reply-to address, which is the event's organizer.
func (s *Service) inviteMessage(ctx context.Context, c Caller, st *ChangeState, iv *Interview, what, note string) (*NewMessage, error) {
	m, err := s.messages()
	if err != nil {
		return nil, err
	}
	reply, err := m.ReplyTo(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	if reply == "" {
		return nil, invalid("invite", "set a reply-to address in the email settings first: it is where the candidate's answer to the invite goes")
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > MaxInviteNote {
		return nil, invalid("invite_note", fmt.Sprintf("keep this under %d characters", MaxInviteNote+1))
	}
	loc, _ := time.LoadLocation(iv.Timezone)
	start := iv.StartsAt.In(loc)
	when := fmt.Sprintf("%s, %s to %s (%s)", start.Format("Monday 2 January 2006"), start.Format("15:04"), iv.EndsAt.In(loc).Format("15:04"), iv.Timezone)
	how := map[string]string{KindVideo: "Video call: ", KindPhone: "Phone call: ", KindOnsite: "At: "}[iv.Kind] + iv.Location
	if iv.Location == "" {
		how = map[string]string{KindPhone: "Phone call: we will call you"}[iv.Kind]
	}
	job := st.JobTitle
	var subject, intro string
	switch what {
	case "invite":
		subject = "Interview for " + job
		intro = fmt.Sprintf("We would like to invite you to an interview for %s at %s.", job, st.Company)
	case "update":
		subject = "Interview for " + job + ": new details"
		intro = fmt.Sprintf("The details of your interview for %s at %s have changed. The new details are below; the calendar event attached replaces the earlier one.", job, st.Company)
	default:
		subject = "Interview for " + job + " cancelled"
		intro = fmt.Sprintf("Your interview for %s at %s has been cancelled.", job, st.Company)
	}
	body := "Hi " + st.Name + ",\n\n" + intro
	if what != "cancel" {
		body += "\n\nWhen: " + when
		if how != "" {
			body += "\n" + how
		}
	}
	if note != "" {
		body += "\n\n" + note
	}
	body += "\n\nA calendar event is attached. Reply to this email if you have any questions."
	ics := ical.Calendar(ical.Event{
		UID: iv.ID, Sequence: iv.Sequence, Start: iv.StartsAt, Duration: time.Duration(iv.DurationMin) * time.Minute,
		Summary: "Interview: " + job + " at " + st.Company, Location: iv.Location, Description: note,
		OrganizerEmail: reply, OrganizerName: st.Company, AttendeeEmail: st.Email, AttendeeName: st.Name,
		Cancelled: what == "cancel", Now: s.now(),
	})
	return &NewMessage{JobID: iv.JobID, AppID: iv.ApplicationID, SenderID: c.UserID, Subject: subject, Body: body, ReplyTo: reply,
		Company: st.Company, ICS: ics, Status: MsgQueued, ScheduledAt: s.now().UTC()}, nil
}

// ScheduleInterview creates an interview and, when asked, emails the candidate the invite in the same step.
func (s *Service) ScheduleInterview(ctx context.Context, c Caller, jobID, appID string, in InterviewInput) (*Interview, error) {
	a, is, err := s.interviewDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	st, err := s.loadChange(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if Final(st.Status) {
		return nil, ErrInvalidTransition
	}
	iv := &Interview{ID: uuid.NewString(), JobID: jobID, ApplicationID: appID, StartsAt: in.StartsAt, DurationMin: in.DurationMin,
		Timezone: in.Timezone, Kind: in.Kind, Location: in.Location, Notes: in.Notes, InterviewerIDs: in.InterviewerIDs,
		Status: InterviewScheduled, CreatedBy: c.UserID}
	if err = s.validateInterview(ctx, c, a, iv, true); err != nil {
		return nil, err
	}
	var msg *NewMessage
	if in.Invite {
		if msg, err = s.inviteMessage(ctx, c, st, iv, "invite", in.InviteNote); err != nil {
			return nil, err
		}
	}
	if err = is.CreateInterview(ctx, c.OrgID, c.UserID, iv, msg); err != nil {
		return nil, err
	}
	return iv, nil
}

// UpdateInterview changes a scheduled interview (reschedule, new place, other interviewers).
func (s *Service) UpdateInterview(ctx context.Context, c Caller, jobID, appID, id string, p InterviewPatch) (*Interview, error) {
	a, is, err := s.interviewDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	iv, err := is.Interview(ctx, c.OrgID, jobID, appID, id)
	if err != nil {
		return nil, err
	}
	if iv.Status != InterviewScheduled {
		return nil, ErrInterviewClosed
	}
	prev, moved := iv.Sequence, false
	if p.StartsAt != nil {
		moved = moved || !p.StartsAt.Equal(iv.StartsAt)
		iv.StartsAt = *p.StartsAt
	}
	if p.DurationMin != nil {
		moved = moved || *p.DurationMin != iv.DurationMin
		iv.DurationMin = *p.DurationMin
	}
	if p.Timezone != nil {
		iv.Timezone = *p.Timezone
	}
	if p.Kind != nil {
		iv.Kind = *p.Kind
	}
	if p.Location != nil {
		iv.Location = *p.Location
	}
	if p.Notes != nil {
		iv.Notes = *p.Notes
	}
	if p.InterviewerIDs != nil {
		iv.InterviewerIDs = *p.InterviewerIDs
	}
	if err = s.validateInterview(ctx, c, a, iv, moved); err != nil {
		return nil, err
	}
	iv.Sequence++
	var msg *NewMessage
	if p.Invite {
		st, lerr := s.loadChange(ctx, c, jobID, appID)
		if lerr != nil {
			return nil, lerr
		}
		if msg, err = s.inviteMessage(ctx, c, st, iv, "update", p.InviteNote); err != nil {
			return nil, err
		}
	}
	if err = is.SaveInterview(ctx, c.OrgID, c.UserID, iv, prev, EventInterviewRescheduled, msg); err != nil {
		return nil, err
	}
	return iv, nil
}

// CancelInterview cancels a scheduled interview and, when asked, tells the candidate (a calendar cancellation).
func (s *Service) CancelInterview(ctx context.Context, c Caller, jobID, appID, id string, notify bool, note string) (*Interview, error) {
	_, is, err := s.interviewDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	iv, err := is.Interview(ctx, c.OrgID, jobID, appID, id)
	if err != nil {
		return nil, err
	}
	if iv.Status != InterviewScheduled {
		return nil, ErrInterviewClosed
	}
	prev := iv.Sequence
	iv.Status, iv.Sequence = InterviewCancelled, iv.Sequence+1
	var msg *NewMessage
	if notify {
		st, lerr := s.loadChange(ctx, c, jobID, appID)
		if lerr != nil {
			return nil, lerr
		}
		if msg, err = s.inviteMessage(ctx, c, st, iv, "cancel", note); err != nil {
			return nil, err
		}
	}
	if err = is.SaveInterview(ctx, c.OrgID, c.UserID, iv, prev, EventInterviewCancelled, msg); err != nil {
		return nil, err
	}
	return iv, nil
}

// CompleteInterview marks a scheduled interview as held.
func (s *Service) CompleteInterview(ctx context.Context, c Caller, jobID, appID, id string) (*Interview, error) {
	_, is, err := s.interviewDecider(ctx, c, jobID, appID)
	if err != nil {
		return nil, err
	}
	if !formschema.IsUUID(id) {
		return nil, ErrNotFound
	}
	iv, err := is.Interview(ctx, c.OrgID, jobID, appID, id)
	if err != nil {
		return nil, err
	}
	if iv.Status != InterviewScheduled {
		return nil, ErrInterviewClosed
	}
	prev := iv.Sequence
	iv.Status, iv.Sequence = InterviewCompleted, iv.Sequence+1
	if err = is.SaveInterview(ctx, c.OrgID, c.UserID, iv, prev, EventInterviewCompleted, nil); err != nil {
		return nil, err
	}
	return iv, nil
}

// MySchedule lists the caller's scheduled interviews in [from, to), soonest first. Interviews of jobs the caller
// can no longer see are left out.
func (s *Service) MySchedule(ctx context.Context, c Caller, from, to time.Time, limit int) ([]Interview, error) {
	is, err := s.interviews()
	if err != nil {
		return nil, err
	}
	if !has(c, rbac.JobViewAll) && !has(c, rbac.JobViewAssigned) {
		return nil, forbidden("you cannot view interviews")
	}
	if from.IsZero() {
		from = s.now()
	}
	if to.IsZero() {
		to = from.AddDate(0, 0, 30)
	}
	if !to.After(from) || to.Sub(from) > MaxScheduleAhead {
		return nil, invalid("to", "choose a range of up to a year")
	}
	if limit <= 0 || limit > MaxScheduleList {
		limit = 50
	}
	list, err := is.MyInterviews(ctx, c.OrgID, c.UserID, from, to, limit)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	checked := map[string]bool{}
	var jobIDs []string
	for _, iv := range list {
		if checked[iv.JobID] {
			continue
		}
		checked[iv.JobID] = true
		if a, aerr := s.Jobs.JobAccess(ctx, c.OrgID, iv.JobID, c.UserID); aerr == nil && canSee(c, a) {
			allowed[iv.JobID] = true
			jobIDs = append(jobIDs, iv.JobID)
		}
	}
	labels := map[string]appjobs.JobLabel{}
	if len(jobIDs) > 0 {
		if l, lerr := s.Jobs.JobLabels(ctx, c.OrgID, jobIDs); lerr == nil {
			labels = l
		}
	}
	out := make([]Interview, 0, len(list))
	for _, iv := range list {
		if !allowed[iv.JobID] {
			continue
		}
		iv.JobCode, iv.JobTitle = labels[iv.JobID].Code, labels[iv.JobID].Title
		out = append(out, iv)
	}
	return out, nil
}
