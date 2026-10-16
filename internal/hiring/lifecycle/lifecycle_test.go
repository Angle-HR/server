package lifecycle

import (
	"errors"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/rbac"
)

func perms(r rbac.Role) rbac.Set { return rbac.NewSet(rbac.DefaultMatrix[r]...) }

func TestNextTable(t *testing.T) {
	cases := []struct {
		from   string
		a      Action
		to     string
		checks bool
		boards bool
	}{
		{jobs.StatusDraft, ActionPublish, jobs.StatusPublished, true, false},
		{jobs.StatusPendingApproval, ActionPublish, jobs.StatusPublished, true, false},
		{jobs.StatusDraft, ActionSubmit, jobs.StatusPendingApproval, false, false},
		{jobs.StatusPendingApproval, ActionWithdraw, jobs.StatusDraft, false, false},
		{jobs.StatusPublished, ActionPause, jobs.StatusPaused, false, true},
		{jobs.StatusPaused, ActionResume, jobs.StatusPublished, true, false},
		{jobs.StatusPublished, ActionClose, jobs.StatusClosed, false, true},
		{jobs.StatusPaused, ActionClose, jobs.StatusClosed, false, true},
		{jobs.StatusClosed, ActionReopen, jobs.StatusPublished, true, false},
		{jobs.StatusExpired, ActionReopen, jobs.StatusPublished, true, false},
		{jobs.StatusPublished, ActionExpire, jobs.StatusExpired, false, true},
		{jobs.StatusClosed, ActionArchive, jobs.StatusArchived, false, false},
		{jobs.StatusExpired, ActionArchive, jobs.StatusArchived, false, false},
		{jobs.StatusPublished, ActionToDraft, jobs.StatusDraft, false, true},
		{jobs.StatusPaused, ActionToDraft, jobs.StatusDraft, false, true},
	}
	for _, c := range cases {
		p, err := Next(c.from, c.a)
		if err != nil {
			t.Errorf("%s from %s: %v", c.a, c.from, err)
			continue
		}
		if p.To != c.to || p.RunsChecks != c.checks || p.RemovesFromBoards != c.boards {
			t.Errorf("%s from %s = %+v", c.a, c.from, p)
		}
	}
}

func TestNextRejectsEveryOtherMove(t *testing.T) {
	allowed := map[string]bool{}
	for a, tr := range table {
		for _, f := range tr.from {
			allowed[f+"|"+string(a)] = true
		}
	}
	statuses := []string{jobs.StatusDraft, jobs.StatusPendingApproval, jobs.StatusPublished, jobs.StatusPaused,
		jobs.StatusClosed, jobs.StatusArchived, jobs.StatusExpired}
	for _, s := range statuses {
		for a := range table {
			_, err := Next(s, a)
			if allowed[s+"|"+string(a)] != (err == nil) {
				t.Errorf("%s/%s: err=%v", s, a, err)
			}
			if err != nil && !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("%s/%s: wrong error %v", s, a, err)
			}
		}
	}
	// Nothing leaves archived.
	for a := range table {
		if _, err := Next(jobs.StatusArchived, a); err == nil {
			t.Errorf("archived job accepted %s", a)
		}
	}
}

func TestParseHidesSystemActions(t *testing.T) {
	if _, err := Parse("expire"); !errors.Is(err, ErrUnknownAction) {
		t.Error("expire must not be requestable")
	}
	if _, err := Parse("nope"); !errors.Is(err, ErrUnknownAction) {
		t.Error("unknown action")
	}
	if a, err := Parse("pause"); err != nil || a != ActionPause {
		t.Error("pause should parse")
	}
}

func TestAuthorize(t *testing.T) {
	hr2 := Caller{UserID: "u2", Perms: perms(rbac.RoleHR2)}
	hr1 := Caller{UserID: "u1", Perms: perms(rbac.RoleHR1)}
	lm := Caller{UserID: "u3", Perms: perms(rbac.RoleLineManager)}

	// HR 2 pauses and closes its own jobs only.
	if _, err := Authorize(hr2, Subject{jobs.StatusPublished, "u2"}, ActionPause); err != nil {
		t.Errorf("hr2 own pause: %v", err)
	}
	if _, err := Authorize(hr2, Subject{jobs.StatusPublished, "u1"}, ActionPause); !errors.Is(err, ErrForbidden) {
		t.Errorf("hr2 other's pause: %v", err)
	}
	// HR 2 cannot publish, resume, reopen or archive, even on its own jobs.
	for _, a := range []Action{ActionResume, ActionReopen, ActionArchive} {
		from := map[Action]string{ActionResume: jobs.StatusPaused, ActionReopen: jobs.StatusClosed,
			ActionArchive: jobs.StatusClosed}[a]
		if _, err := Authorize(hr2, Subject{from, "u2"}, a); !errors.Is(err, ErrForbidden) {
			t.Errorf("hr2 %s: %v", a, err)
		}
	}
	// HR 1 acts on anyone's jobs.
	if _, err := Authorize(hr1, Subject{jobs.StatusClosed, "u2"}, ActionReopen); err != nil {
		t.Errorf("hr1 reopen: %v", err)
	}
	// Line manager can do nothing.
	if _, err := Authorize(lm, Subject{jobs.StatusPublished, "u3"}, ActionClose); !errors.Is(err, ErrForbidden) {
		t.Errorf("line manager close: %v", err)
	}
	// Wrong status beats permission in the message order: an invalid move is reported as invalid.
	if _, err := Authorize(hr1, Subject{jobs.StatusDraft, "u1"}, ActionPause); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("draft pause: %v", err)
	}
	if _, err := Authorize(hr1, Subject{jobs.StatusPublished, "u1"}, ActionExpire); !errors.Is(err, ErrUnknownAction) {
		t.Errorf("person expiring a job: %v", err)
	}
}

func TestPublishAction(t *testing.T) {
	hr2 := Caller{UserID: "u2", Perms: perms(rbac.RoleHR2)}
	hr1 := Caller{UserID: "u1", Perms: perms(rbac.RoleHR1)}
	if PublishAction(hr1, jobs.StatusDraft) != ActionPublish {
		t.Error("hr1 publishes")
	}
	if PublishAction(hr2, jobs.StatusDraft) != ActionSubmit {
		t.Error("hr2 submits for approval")
	}
}

func TestBulkPlan(t *testing.T) {
	hr2 := Caller{UserID: "u2", Perms: perms(rbac.RoleHR2)}
	items := []BulkItem{
		{"a", jobs.StatusPublished, "u2"},
		{"b", jobs.StatusPublished, "u1"}, // not hr2's
		{"c", jobs.StatusDraft, "u2"},     // wrong status
		{"a", jobs.StatusPublished, "u2"}, // duplicate
	}
	ok, skipped, err := BulkPlan(hr2, items, ActionPause)
	if err != nil {
		t.Fatal(err)
	}
	if len(ok) != 1 || ok[0].ID != "a" {
		t.Errorf("ok = %+v", ok)
	}
	if len(skipped) != 2 || skipped[0].ID != "b" || skipped[1].ID != "c" {
		t.Errorf("skipped = %+v", skipped)
	}
	if _, _, err := BulkPlan(hr2, nil, ActionPause); err == nil {
		t.Error("empty selection")
	}
	if _, _, err := BulkPlan(hr2, items, Action("expire")); !errors.Is(err, ErrUnknownAction) {
		t.Error("expire in bulk")
	}
	if BulkActions[ActionPublish] {
		t.Error("publish is one job at a time")
	}
	if !BulkActions[ActionResume] || !BulkActions[ActionReopen] {
		t.Error("resume and reopen are allowed in bulk")
	}
}
