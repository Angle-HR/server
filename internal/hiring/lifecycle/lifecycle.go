// Package lifecycle is the job status machine: which action moves a job from one status to the next, who may
// do it and whether the publish checks run again. It is pure logic with no database, so every transition is
// table-tested. Stored statuses are the ones in hiring.job_postings; the UI label "Open" is "published".
package lifecycle

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/rbac"
)

// Action is something a person (or the closing-date task) does to a job's status.
type Action string

// Actions. Publish, Resume and Reopen all end in "published" and all re-run the publish checks; they differ
// in where the job starts.
const (
	ActionSubmit   Action = "submit"   // draft -> pending_approval (the caller cannot publish)
	ActionWithdraw Action = "withdraw" // pending_approval -> draft (author withdraws, or an approver sends it back)
	ActionPublish  Action = "publish"  // draft or pending_approval -> published
	ActionPause    Action = "pause"    // published -> paused
	ActionResume   Action = "resume"   // paused -> published
	ActionClose    Action = "close"    // published or paused -> closed
	ActionReopen   Action = "reopen"   // closed or expired -> published
	ActionExpire   Action = "expire"   // published -> expired (closing date passed; system only)
	ActionArchive  Action = "archive"  // closed or expired -> archived
	ActionToDraft  Action = "to_draft" // published or paused -> draft
)

// Errors the HTTP layer maps to responses.
var (
	// ErrUnknownAction means the action name is not one of the actions above.
	ErrUnknownAction = errors.New("lifecycle: unknown action")
	// ErrInvalidTransition means the job's status does not allow the action.
	ErrInvalidTransition = errors.New("lifecycle: action not allowed from this status")
	// ErrForbidden means the caller lacks the permission the action needs.
	ErrForbidden = errors.New("lifecycle: not permitted")
)

type transition struct {
	from []string
	to   string
	perm rbac.Permission
	// runsChecks: the publish checks (verified company, accepted DPA, gates...) must pass before the move.
	runsChecks bool
	// removesFromBoards: the job stops being public and every channel is told.
	removesFromBoards bool
	// systemOnly: no person can request it through the API.
	systemOnly bool
}

var table = map[Action]transition{
	ActionSubmit: {
		from: []string{jobs.StatusDraft}, to: jobs.StatusPendingApproval, perm: rbac.JobEditDraft,
	},
	ActionWithdraw: {
		from: []string{jobs.StatusPendingApproval}, to: jobs.StatusDraft, perm: rbac.JobEditDraft,
	},
	ActionPublish: {
		from: []string{jobs.StatusDraft, jobs.StatusPendingApproval}, to: jobs.StatusPublished,
		perm: rbac.JobPublishExternal, runsChecks: true,
	},
	ActionPause: {
		from: []string{jobs.StatusPublished}, to: jobs.StatusPaused,
		perm: rbac.JobStatusChange, removesFromBoards: true,
	},
	ActionResume: {
		from: []string{jobs.StatusPaused}, to: jobs.StatusPublished,
		perm: rbac.JobPublishExternal, runsChecks: true,
	},
	ActionClose: {
		from: []string{jobs.StatusPublished, jobs.StatusPaused}, to: jobs.StatusClosed,
		perm: rbac.JobStatusChange, removesFromBoards: true,
	},
	ActionReopen: {
		from: []string{jobs.StatusClosed, jobs.StatusExpired}, to: jobs.StatusPublished,
		perm: rbac.JobPublishExternal, runsChecks: true,
	},
	ActionExpire: {
		from: []string{jobs.StatusPublished}, to: jobs.StatusExpired,
		perm: rbac.JobStatusChange, removesFromBoards: true, systemOnly: true,
	},
	ActionArchive: {
		from: []string{jobs.StatusClosed, jobs.StatusExpired}, to: jobs.StatusArchived, perm: rbac.JobArchive,
	},
	ActionToDraft: {
		from: []string{jobs.StatusPublished, jobs.StatusPaused}, to: jobs.StatusDraft,
		perm: rbac.JobStatusChange, removesFromBoards: true,
	},
}

// Parse turns a request value into an Action. System-only actions are not parseable from the API.
func Parse(name string) (Action, error) {
	a := Action(name)
	t, ok := table[a]
	if !ok || t.systemOnly {
		return "", fmt.Errorf("%w: %q", ErrUnknownAction, name)
	}
	return a, nil
}

// Plan is what an allowed action does.
type Plan struct {
	Action            Action
	From              string
	To                string
	RunsChecks        bool
	RemovesFromBoards bool
}

// Subject is the job being changed, as far as the status rules care.
type Subject struct {
	Status    string
	CreatedBy string
}

// Caller is who acts.
type Caller struct {
	UserID string
	Perms  rbac.Set
}

// Next works out the move without checking permissions: used for the system's own actions and to explain a
// refusal.
func Next(status string, a Action) (*Plan, error) {
	t, ok := table[a]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAction, string(a))
	}
	for _, f := range t.from {
		if f == status {
			return &Plan{Action: a, From: status, To: t.to, RunsChecks: t.runsChecks,
				RemovesFromBoards: t.removesFromBoards}, nil
		}
	}
	return nil, fmt.Errorf("%w: cannot %s a %s job", ErrInvalidTransition, a, status)
}

// Authorize checks a person's action on a job. Without job.manage_any a caller may change only the jobs they
// created (HR 2 on its own jobs). A caller who cannot see the job at all gets ErrForbidden as well; the
// service turns that into "not found" before it gets here.
func Authorize(c Caller, s Subject, a Action) (*Plan, error) {
	t, ok := table[a]
	if !ok || t.systemOnly {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAction, string(a))
	}
	plan, err := Next(s.Status, a)
	if err != nil {
		return nil, err
	}
	if !c.Perms.Has(t.perm) {
		return nil, fmt.Errorf("%w: %s needs %s", ErrForbidden, a, t.perm)
	}
	if s.CreatedBy != c.UserID && !c.Perms.Has(rbac.JobManageAny) {
		return nil, fmt.Errorf("%w: you can only %s your own jobs", ErrForbidden, a)
	}
	return plan, nil
}

// PublishAction picks the action for "publish this job" given who asks: people who may publish publish,
// everyone else submits for approval (the status between draft and published).
func PublishAction(c Caller, status string) Action {
	if c.Perms.Has(rbac.JobPublishExternal) {
		return ActionPublish
	}
	if status == jobs.StatusDraft {
		return ActionSubmit
	}
	return ActionPublish
}

// ---- bulk ----

// BulkItem is one selected job.
type BulkItem struct {
	ID        string
	Status    string
	CreatedBy string
}

// Skipped explains why one selected job was left alone.
type Skipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// MaxBulk bounds a selection.
const MaxBulk = 100

// BulkPlan splits a selection into the jobs the action applies to and the ones it does not. Nothing is
// all-or-nothing: a mixed selection acts on what it can and reports the rest, and the caller decides whether
// to undo. Duplicate ids are ignored.
func BulkPlan(c Caller, items []BulkItem, a Action) (ok []BulkItem, skipped []Skipped, err error) {
	if len(items) == 0 || len(items) > MaxBulk {
		return nil, nil, fmt.Errorf("lifecycle: select between 1 and %d jobs", MaxBulk)
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if seen[it.ID] {
			continue
		}
		seen[it.ID] = true
		if _, aerr := Authorize(c, Subject{Status: it.Status, CreatedBy: it.CreatedBy}, a); aerr != nil {
			if errors.Is(aerr, ErrUnknownAction) {
				return nil, nil, aerr
			}
			skipped = append(skipped, Skipped{ID: it.ID, Reason: aerr.Error()})
			continue
		}
		ok = append(ok, it)
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ID < skipped[j].ID })
	return ok, skipped, nil
}

// BulkActions are the actions the bulk endpoint accepts. Publishing stays one job at a time. Resume and reopen
// are allowed in bulk: each job still runs its own publish checks and writes its own compliance log, and a job
// that fails its checks is skipped with the reason while the rest go ahead.
var BulkActions = map[Action]bool{
	ActionPause: true, ActionResume: true, ActionClose: true, ActionReopen: true,
	ActionArchive: true, ActionToDraft: true,
}
