package applications

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/reapply"
	"github.com/Angle-HR/server/internal/rbac"
)

const (
	tenant = "11111111-1111-4111-8111-111111111111"
	job    = "22222222-2222-4222-8222-222222222222"
	app    = "33333333-3333-4333-8333-333333333333"
	file   = "44444444-4444-4444-8444-444444444444"
	owner  = "55555555-5555-4555-8555-555555555555"
	other  = "66666666-6666-4666-8666-666666666666"
)

type fakeStore struct {
	access    *JobAccess
	accessErr error
	state     *ChangeState
	changes   []*Change
	changeErr error
	opened    []OpenInput
	listQ     StoreListQuery
	items     []Summary
	months    int
	emails    bool
	saved     bool
}

func (f *fakeStore) JobAccess(_ context.Context, _, _, user string) (*JobAccess, error) {
	if f.accessErr != nil {
		return nil, f.accessErr
	}
	a := *f.access
	if user == other {
		a.MemberRole = f.access.MemberRole
	}
	return &a, nil
}
func (f *fakeStore) List(_ context.Context, _, _ string, q StoreListQuery) ([]Summary, map[string]int, error) {
	f.listQ = q
	return f.items, map[string]int{"received": len(f.items)}, nil
}
func (f *fakeStore) Open(_ context.Context, _ string, in OpenInput) (*Detail, error) {
	f.opened = append(f.opened, in)
	return &Detail{Summary: Summary{ID: in.AppID}}, nil
}
func (f *fakeStore) FileInfo(_ context.Context, _, jobID, _, fileID, _ string) (*FileInfo, error) {
	return &FileInfo{ID: fileID, Filename: "cv.pdf", ContentType: "application/pdf", JobID: jobID}, nil
}
func (f *fakeStore) LoadChange(context.Context, string, string, string) (*ChangeState, error) {
	if f.state == nil {
		return nil, hiringtypes.ErrNotFound
	}
	s := *f.state
	return &s, nil
}
func (f *fakeStore) ApplyChange(_ context.Context, _ string, ch *Change) error {
	if f.changeErr != nil {
		return f.changeErr
	}
	f.changes = append(f.changes, ch)
	return nil
}
func (f *fakeStore) GetApplicantSettings(context.Context, string) (int, bool, error) {
	return f.months, f.emails, nil
}
func (f *fakeStore) SaveApplicantSettings(_ context.Context, _ string, m int, e bool) error {
	f.months, f.emails, f.saved = m, e, true
	return nil
}

type fakeFiles struct{ key string }

func (f *fakeFiles) PresignGet(_ context.Context, key string, _ time.Duration, name, _ string) (string, error) {
	f.key = key
	return "https://r2.test/" + name, nil
}

func caller(user string, perms ...rbac.Permission) Caller {
	return Caller{UserID: user, OrgID: tenant, Perms: rbac.NewSet(perms...)}
}

func newSvc(t *testing.T, role string) (*Service, *fakeStore) {
	t.Helper()
	st := &fakeStore{
		access: &JobAccess{JobID: job, Code: "JB-1", Title: "Engineer", Status: "published", CreatedBy: owner,
			MemberRole: role, RetentionMonths: 6},
		state: &ChangeState{Status: StatusInReview, Name: "Ada", Email: "ada@example.com", JobTitle: "Engineer",
			JobCode: "JB-1", Company: "Acme", PublicID: "abc", SendStatusEmails: true},
		emails: true,
	}
	h, err := reapply.New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Store: st, Files: &fakeFiles{}, Hasher: h,
		NoticeURL: func(id string) string { return "https://x.test/j/" + id + "/privacy" },
		Now:       func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }}, st
}

func TestAccessFollowsTheJob(t *testing.T) {
	s, _ := newSvc(t, "")
	ctx := context.Background()
	if _, err := s.List(ctx, caller(other), job, ListQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no permission: want not found, got %v", err)
	}
	if _, err := s.List(ctx, caller(other, rbac.JobViewAssigned), job, ListQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("assigned permission but not on the job: want not found, got %v", err)
	}
	if _, err := s.List(ctx, caller(owner, rbac.JobViewAssigned), job, ListQuery{}); err != nil {
		t.Fatalf("creator: %v", err)
	}
	if _, err := s.List(ctx, caller(other, rbac.JobViewAll), job, ListQuery{}); err != nil {
		t.Fatalf("view all: %v", err)
	}
	s2, _ := newSvc(t, hiringtypes.MemberInterviewer)
	if _, err := s2.List(ctx, caller(other, rbac.JobViewAssigned), job, ListQuery{}); err != nil {
		t.Fatalf("team member: %v", err)
	}
	if _, err := s.List(ctx, caller(owner, rbac.JobViewAll), "not-a-uuid", ListQuery{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestListCursorAndFilters(t *testing.T) {
	s, st := newSvc(t, "")
	base := time.Date(2026, 10, 1, 0, 0, 0, 123456000, time.UTC)
	for _, id := range []string{app, file, owner} {
		st.items = append(st.items, Summary{ID: id, SubmittedAt: base})
	}
	c := caller(owner, rbac.JobViewAll)
	p, err := s.List(context.Background(), c, job, ListQuery{Limit: 2, Statuses: []string{StatusFlagged}, Query: " ada "})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 2 || p.NextCursor == "" || st.listQ.Limit != 3 || st.listQ.Search != "ada" {
		t.Fatalf("page %+v query %+v", p, st.listQ)
	}
	st.items = st.items[:1]
	if _, err = s.List(context.Background(), c, job, ListQuery{Cursor: p.NextCursor}); err != nil {
		t.Fatal(err)
	}
	if !st.listQ.AfterTime.Equal(base) || st.listQ.AfterID != file {
		t.Fatalf("cursor not decoded: %+v", st.listQ)
	}
	if _, err = s.List(context.Background(), c, job, ListQuery{Cursor: "!!"}); err == nil {
		t.Fatal("bad cursor accepted")
	}
	var ve *draft.ValidationError
	if _, err = s.List(context.Background(), c, job, ListQuery{Statuses: []string{"nope"}}); !errors.As(err, &ve) {
		t.Fatalf("bad status: %v", err)
	}
	p, _ = s.List(context.Background(), c, job, ListQuery{Limit: 1000})
	if st.listQ.Limit != 101 || p.Items == nil {
		t.Fatalf("limit not capped: %d", st.listQ.Limit)
	}
}

func TestGetTracksReviewOnlyForDeciders(t *testing.T) {
	s, st := newSvc(t, hiringtypes.MemberInterviewer)
	if _, err := s.Get(context.Background(), caller(other, rbac.JobViewAssigned), job, app); err != nil {
		t.Fatal(err)
	}
	if st.opened[0].TrackReview {
		t.Fatal("an interviewer must not become the reviewer")
	}
	s, st = newSvc(t, hiringtypes.MemberRecruiter)
	d, err := s.Get(context.Background(), caller(other, rbac.JobViewAssigned), job, app)
	if err != nil || !st.opened[0].TrackReview || !d.CanDecide {
		t.Fatalf("recruiter: %v %+v", err, st.opened)
	}
}

func TestSetStatusTransitions(t *testing.T) {
	ctx := context.Background()
	c := caller(owner, rbac.JobViewAssigned) // the creator
	cases := []struct {
		from, to string
		want     error
	}{
		{StatusReceived, StatusInReview, nil},
		{StatusReceived, StatusWithdrawn, nil},
		{StatusReceived, StatusHired, ErrInvalidTransition},
		{StatusReceived, StatusRejected, ErrInvalidTransition},
		{StatusInReview, StatusRejected, nil},
		{StatusInReview, StatusHired, nil},
		{StatusInReview, StatusWithdrawn, nil},
		{StatusInReview, StatusReceived, ErrInvalidTransition},
		{StatusInReview, StatusFlagged, ErrInvalidTransition},
		{StatusFlagged, StatusInReview, ErrReviewRequired},
		{StatusFlagged, StatusRejected, ErrReviewRequired},
		{StatusFlagged, StatusHired, ErrReviewRequired},
		{StatusFlagged, StatusWithdrawn, nil},
		{StatusRejected, StatusInReview, ErrInvalidTransition},
		{StatusHired, StatusRejected, ErrInvalidTransition},
		{StatusWithdrawn, StatusReceived, ErrInvalidTransition},
	}
	for _, tc := range cases {
		s, st := newSvc(t, "")
		st.state.Status = tc.from
		_, err := s.SetStatus(ctx, c, job, app, StatusInput{Status: tc.to})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s -> %s: want %v, got %v", tc.from, tc.to, tc.want, err)
		}
		if tc.want != nil && len(st.changes) != 0 {
			t.Errorf("%s -> %s: a refused change was written", tc.from, tc.to)
		}
	}
}

func TestOnlyDecidersChangeStatus(t *testing.T) {
	ctx := context.Background()
	for role, ok := range map[string]bool{
		hiringtypes.MemberInterviewer: false, hiringtypes.MemberViewer: false,
		hiringtypes.MemberRecruiter: true, hiringtypes.MemberHiringManager: true,
	} {
		s, _ := newSvc(t, role)
		_, err := s.SetStatus(ctx, caller(other, rbac.JobViewAssigned), job, app, StatusInput{Status: StatusRejected})
		var fe *draft.ForbiddenError
		if ok && err != nil || !ok && !errors.As(err, &fe) {
			t.Errorf("%s: ok=%v err=%v", role, ok, err)
		}
	}
	s, _ := newSvc(t, "")
	if _, err := s.SetStatus(ctx, caller(other, rbac.JobViewAll, rbac.JobManageAny), job, app, StatusInput{Status: StatusHired}); err != nil {
		t.Fatalf("manage any: %v", err)
	}
	if _, err := s.SetStatus(ctx, caller(other, rbac.JobViewAll), job, app, StatusInput{Status: StatusHired}); err == nil {
		t.Fatal("view all alone must not decide")
	}
}

func TestRejectionStartsRetentionBlocksAndEmails(t *testing.T) {
	s, st := newSvc(t, "")
	st.state.ReapplyBlockMonths = 6
	res, err := s.SetStatus(context.Background(), caller(owner, rbac.JobViewAssigned), job, app,
		StatusInput{Status: StatusRejected, Note: "  not a fit  "})
	if err != nil || res.DecidedAt == nil {
		t.Fatalf("%v %+v", err, res)
	}
	ch := st.changes[0]
	if ch.Note != "not a fit" || ch.RetentionMonths != 6 || ch.From != StatusInReview {
		t.Fatalf("%+v", ch)
	}
	if ch.Block == nil || len(ch.Block.EmailHMAC) != 64 || strings.Contains(ch.Block.EmailHMAC, "ada") ||
		!ch.Block.Until.Equal(ch.Now.AddDate(0, 6, 0)) {
		t.Fatalf("block %+v", ch.Block)
	}
	if ch.Email == nil || ch.Email.Kind != "application_rejected" || ch.Email.Recipient != "ada@example.com" ||
		ch.Email.Data["notice_url"] != "https://x.test/j/abc/privacy" {
		t.Fatalf("email %+v", ch.Email)
	}
}

func TestHireEmailsButNeverBlocks(t *testing.T) {
	s, st := newSvc(t, "")
	st.state.ReapplyBlockMonths = 6
	if _, err := s.SetStatus(context.Background(), caller(owner, rbac.JobViewAssigned), job, app, StatusInput{Status: StatusHired}); err != nil {
		t.Fatal(err)
	}
	ch := st.changes[0]
	if ch.Block != nil || ch.Email == nil || ch.Email.Kind != "application_hired" {
		t.Fatalf("%+v", ch)
	}
}

func TestNoBlockWhenOffOrNoKey(t *testing.T) {
	s, st := newSvc(t, "")
	st.state.SendStatusEmails = false
	if _, err := s.SetStatus(context.Background(), caller(owner, rbac.JobViewAssigned), job, app, StatusInput{Status: StatusRejected}); err != nil {
		t.Fatal(err)
	}
	if ch := st.changes[0]; ch.Block != nil || ch.Email != nil {
		t.Fatalf("setting off: %+v", ch)
	}
	s, st = newSvc(t, "")
	s.Hasher = nil
	st.state.ReapplyBlockMonths = 6
	if _, err := s.SetStatus(context.Background(), caller(owner, rbac.JobViewAssigned), job, app, StatusInput{Status: StatusRejected}); err != nil {
		t.Fatal(err)
	}
	if st.changes[0].Block != nil {
		t.Fatal("a block was recorded without a key")
	}
}

func TestNoteLength(t *testing.T) {
	s, _ := newSvc(t, "")
	_, err := s.SetStatus(context.Background(), caller(owner, rbac.JobViewAssigned), job, app,
		StatusInput{Status: StatusHired, Note: strings.Repeat("x", MaxNote+1)})
	var ve *draft.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("%v", err)
	}
}

func TestReview(t *testing.T) {
	ctx := context.Background()
	c := caller(owner, rbac.JobViewAssigned)
	flagged := func() (*Service, *fakeStore) {
		s, st := newSvc(t, "")
		st.state.Status = StatusFlagged
		st.state.Review = &ReviewState{Open: true, OpenedBy: owner}
		return s, st
	}
	s, st := flagged()
	res, err := s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionProceed, Note: "fine"})
	if err != nil || res.Status != StatusInReview || res.DecidedAt != nil {
		t.Fatalf("proceed: %v %+v", err, res)
	}
	if ch := st.changes[0]; ch.Decision != DecisionProceed || ch.From != StatusFlagged || ch.Email != nil || ch.Block != nil {
		t.Fatalf("%+v", ch)
	}
	s, st = flagged()
	res, err = s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionReject})
	if err != nil || res.Status != StatusRejected || res.DecidedAt == nil || st.changes[0].Email == nil {
		t.Fatalf("reject: %v %+v", err, res)
	}
	s, _ = flagged()
	if _, err = s.Review(ctx, caller(owner, rbac.JobViewAssigned), job, app, ReviewInput{Decision: "maybe"}); err == nil {
		t.Fatal("bad decision accepted")
	}
	// somebody else opened it last
	s, st = flagged()
	st.state.Review.OpenedBy = other
	if _, err = s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionReject}); !errors.Is(err, ErrNotOpened) {
		t.Fatalf("want not opened: %v", err)
	}
	// nobody opened it
	s, st = flagged()
	st.state.Review.OpenedBy = ""
	if _, err = s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionReject}); !errors.Is(err, ErrNotOpened) {
		t.Fatalf("want not opened: %v", err)
	}
	// not flagged, or review already done
	s, st = flagged()
	st.state.Status = StatusReceived
	if _, err = s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionProceed}); !errors.Is(err, ErrNoReview) {
		t.Fatalf("want no review: %v", err)
	}
	s, st = flagged()
	st.state.Review.Open = false
	if _, err = s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionProceed}); !errors.Is(err, ErrNoReview) {
		t.Fatalf("want no review: %v", err)
	}
	// an interviewer cannot review
	s, st = newSvc(t, hiringtypes.MemberInterviewer)
	st.state.Status = StatusFlagged
	var fe *draft.ForbiddenError
	if _, err = s.Review(ctx, caller(other, rbac.JobViewAssigned), job, app, ReviewInput{Decision: DecisionProceed}); !errors.As(err, &fe) {
		t.Fatalf("want forbidden: %v", err)
	}
	// the store refuses a stale change
	s, st = flagged()
	st.changeErr = ErrStale
	if _, err = s.Review(ctx, c, job, app, ReviewInput{Decision: DecisionProceed}); !errors.Is(err, ErrStale) {
		t.Fatalf("want stale: %v", err)
	}
}

func TestFileLink(t *testing.T) {
	s, _ := newSvc(t, "")
	l, err := s.File(context.Background(), caller(owner, rbac.JobViewAssigned), job, app, file)
	if err != nil {
		t.Fatal(err)
	}
	if l.URL != "https://r2.test/cv.pdf" || !l.ExpiresAt.Equal(s.Now().Add(5*time.Minute)) {
		t.Fatalf("%+v", l)
	}
	if got := s.Files.(*fakeFiles).key; got != "f/"+tenant+"/"+job+"/"+file {
		t.Fatalf("key %s", got)
	}
	if _, err = s.File(context.Background(), caller(other), job, app, file); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	s.Files = nil
	if _, err = s.File(context.Background(), caller(owner, rbac.JobViewAssigned), job, app, file); err == nil {
		t.Fatal("no storage must fail")
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, st := newSvc(t, "")
	six, off := 6, false
	var fe *draft.ForbiddenError
	if _, err := s.SaveSettings(ctx, caller(owner, rbac.JobCreate), SettingsInput{ReapplyBlockMonths: &six}); !errors.As(err, &fe) {
		t.Fatalf("want forbidden: %v", err)
	}
	got, err := s.SaveSettings(ctx, caller(owner, rbac.JobRetentionSet), SettingsInput{ReapplyBlockMonths: &six, SendStatusEmails: &off})
	if err != nil || got.ReapplyBlockMonths != 6 || got.SendStatusEmails || !st.saved || !got.ReapplyBlockAvailable {
		t.Fatalf("%v %+v", err, got)
	}
	three := 3
	var ve *draft.ValidationError
	if _, err = s.SaveSettings(ctx, caller(owner, rbac.JobRetentionSet), SettingsInput{ReapplyBlockMonths: &three}); !errors.As(err, &ve) {
		t.Fatalf("want validation: %v", err)
	}
	s.Hasher = nil
	if _, err = s.SaveSettings(ctx, caller(owner, rbac.JobRetentionSet), SettingsInput{ReapplyBlockMonths: &six}); !errors.As(err, &ve) {
		t.Fatalf("no key must refuse 6: %v", err)
	}
	zero := 0
	if _, err = s.SaveSettings(ctx, caller(owner, rbac.JobRetentionSet), SettingsInput{ReapplyBlockMonths: &zero}); err != nil {
		t.Fatalf("turning off must work without a key: %v", err)
	}
}
