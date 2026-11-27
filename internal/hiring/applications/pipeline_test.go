package applications

import (
	"context"
	"errors"
	"testing"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
)

const (
	stageApplied = "77777777-7777-4777-8777-777777777771"
	stageInter   = "77777777-7777-4777-8777-777777777772"
	stageHired   = "77777777-7777-4777-8777-777777777773"
	stageDisq    = "77777777-7777-4777-8777-777777777774"
	note1        = "88888888-8888-4888-8888-888888888881"
)

type fakePipeline struct {
	PipelineStore // methods a test does not use
	status        string
	stageID       string
	stages        map[string]Stage
	moved         []*OpenMove
	tagsSet       []string
	followed      *bool
	notesAuthor   string
}

func newPipeline() *fakePipeline {
	return &fakePipeline{status: StatusReceived, stageID: stageApplied, stages: map[string]Stage{
		stageApplied: {ID: stageApplied, Name: "Applied", Kind: KindApplied},
		stageInter:   {ID: stageInter, Name: "Interview", Kind: KindInterview},
		stageHired:   {ID: stageHired, Name: "Hired", Kind: KindHired, Final: true},
		stageDisq:    {ID: stageDisq, Name: "Disqualified", Kind: KindDisqualified, Final: true},
	}}
}

func (f *fakePipeline) Stages(context.Context, string, string) ([]Stage, error) {
	var out []Stage
	for _, s := range f.stages {
		out = append(out, s)
	}
	return out, nil
}
func (f *fakePipeline) MoveState(_ context.Context, _, _, _, stageID string) (*MoveState, error) {
	t, ok := f.stages[stageID]
	if !ok {
		return nil, ErrNotFound
	}
	return &MoveState{Status: f.status, StageID: f.stageID, Target: t}, nil
}
func (f *fakePipeline) MoveToOpenStage(_ context.Context, _ string, m *OpenMove) error {
	f.moved = append(f.moved, m)
	return nil
}
func (f *fakePipeline) SetTags(_ context.Context, _, _, _, _ string, ids []string) ([]Tag, error) {
	f.tagsSet = ids
	return []Tag{}, nil
}
func (f *fakePipeline) Follow(_ context.Context, _, _, _, _ string, on bool) error {
	f.followed = &on
	return nil
}
func (f *fakePipeline) EditNote(_ context.Context, _, _, _, _, author, body string) (*Note, error) {
	if author != f.notesAuthor {
		return nil, ErrNotFound
	}
	return &Note{ID: note1, Body: body, Mine: true}, nil
}

func pipelineSvc(t *testing.T, role string) (*Service, *fakeStore, *fakePipeline) {
	t.Helper()
	s, st := newSvc(t, role)
	p := newPipeline()
	s.Pipeline = p
	return s, st, p
}

func decider() Caller { return caller(owner, rbac.JobViewAll, rbac.JobViewAssigned) }

func TestMoveToOpenStageMakesReceivedInReview(t *testing.T) {
	s, st, p := pipelineSvc(t, "")
	res, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageInter})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusInReview || res.StageID != stageInter {
		t.Errorf("result %+v", res)
	}
	if len(p.moved) != 1 || p.moved[0].FromStatus != StatusReceived || p.moved[0].ToStatus != StatusInReview {
		t.Errorf("move %+v", p.moved)
	}
	if len(st.changes) != 0 {
		t.Error("a move to an open stage is not a status decision")
	}
}

func TestMoveBackToAppliedKeepsStatus(t *testing.T) {
	s, _, p := pipelineSvc(t, "")
	p.status, p.stageID = StatusInReview, stageInter
	res, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageApplied})
	if err != nil || res.Status != StatusInReview {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestMoveToSameStageIsANoOp(t *testing.T) {
	s, _, p := pipelineSvc(t, "")
	if _, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageApplied}); err != nil {
		t.Fatal(err)
	}
	if len(p.moved) != 0 {
		t.Error("nothing to write")
	}
}

func TestFlaggedAndFinalApplicationsDoNotMove(t *testing.T) {
	s, _, p := pipelineSvc(t, "")
	p.status = StatusFlagged
	if _, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageInter}); !errors.Is(err, ErrReviewRequired) {
		t.Errorf("flagged: %v", err)
	}
	for _, st := range []string{StatusRejected, StatusHired, StatusWithdrawn} {
		p.status = st
		if _, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageInter}); !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s: %v", st, err)
		}
	}
}

func TestDisqualifyNeedsAReasonAndDecides(t *testing.T) {
	s, st, _ := pipelineSvc(t, "")
	st.state.Status = StatusReceived
	if _, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageDisq}); !isCode(err, apperror.CodeValidationError) {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageDisq, Reason: "made-up"}); !isCode(err, apperror.CodeValidationError) {
		t.Fatalf("bad reason: %v", err)
	}
	res, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageDisq, Reason: "not_a_fit", Note: "thanks"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusRejected || res.DecidedAt == nil || res.StageID != stageDisq {
		t.Errorf("result %+v", res)
	}
	ch := st.changes[0]
	if ch.From != StatusReceived || ch.To != StatusRejected || ch.StageID != stageDisq || ch.Reason != "not_a_fit" || ch.Note != "thanks" {
		t.Errorf("change %+v", ch)
	}
	if ch.Email == nil {
		t.Error("the candidate is emailed as for any rejection")
	}
}

func TestHireThroughTheStage(t *testing.T) {
	s, st, _ := pipelineSvc(t, "")
	st.state.Status = StatusInReview
	res, err := s.MoveStage(context.Background(), decider(), job, app, MoveInput{StageID: stageHired})
	if err != nil || res.Status != StatusHired {
		t.Fatalf("%+v %v", res, err)
	}
	if st.changes[0].Reason != "" || st.changes[0].StageID != stageHired {
		t.Errorf("change %+v", st.changes[0])
	}
}

func TestOnlyDecidersMoveApplications(t *testing.T) {
	s, _, _ := pipelineSvc(t, "interviewer")
	c := caller(other, rbac.JobViewAssigned)
	if _, err := s.MoveStage(context.Background(), c, job, app, MoveInput{StageID: stageInter}); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("interviewer: %v", err)
	}
	if err := s.CreateStageErr(c); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("interviewer creates stage: %v", err)
	}
}

// CreateStageErr is a test helper that calls CreateStage and returns only the error.
func (s *Service) CreateStageErr(c Caller) error {
	_, err := s.CreateStage(context.Background(), c, job, "Phone screen")
	return err
}

func TestBulkMoveReportsEachApplication(t *testing.T) {
	s, _, p := pipelineSvc(t, "")
	ids := []string{app, app, "not-a-uuid"}
	items, err := s.BulkMoveStage(context.Background(), decider(), job, ids, MoveInput{StageID: stageInter})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !items[0].OK || items[1].OK || items[1].Reason != "not_found" {
		t.Errorf("items %+v", items)
	}
	if len(p.moved) != 1 {
		t.Errorf("duplicates move once: %d", len(p.moved))
	}
	if _, err = s.BulkMoveStage(context.Background(), decider(), job, nil, MoveInput{StageID: stageInter}); err == nil {
		t.Error("an empty bulk is refused")
	}
	if _, err = s.BulkMoveStage(context.Background(), decider(), job, ids, MoveInput{StageID: stageDisq}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("a bulk disqualify without a reason fails as a whole: %v", err)
	}
}

func TestBulkMoveSkipsFlagged(t *testing.T) {
	s, _, p := pipelineSvc(t, "")
	p.status = StatusFlagged
	items, err := s.BulkMoveStage(context.Background(), decider(), job, []string{app}, MoveInput{StageID: stageInter})
	if err != nil || items[0].OK || items[0].Reason != "flagged" {
		t.Errorf("%+v %v", items, err)
	}
}

func TestNotesAndFollowFollowTheJobAccess(t *testing.T) {
	s, _, p := pipelineSvc(t, "interviewer")
	viewer := caller(other, rbac.JobViewAssigned)
	if err := s.Follow(context.Background(), viewer, job, app, true); err != nil || p.followed == nil || !*p.followed {
		t.Errorf("a viewer can follow: %v", err)
	}
	if _, err := s.AddNote(context.Background(), viewer, job, app, "   "); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("empty note: %v", err)
	}
	p.notesAuthor = other
	if _, err := s.EditNote(context.Background(), viewer, job, app, note1, "better"); err != nil {
		t.Errorf("author edits: %v", err)
	}
	if _, err := s.EditNote(context.Background(), caller(owner, rbac.JobViewAll), job, app, note1, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else's note: %v", err)
	}
	if err := s.Follow(context.Background(), caller("99999999-9999-4999-8999-999999999999"), job, app, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("no access to the job: %v", err)
	}
}

func TestTagsPermissionsAndLimits(t *testing.T) {
	s, _, p := pipelineSvc(t, "")
	ctx := context.Background()
	if _, err := s.CreateTag(ctx, decider(), "VIP", ""); !isCode(err, apperror.CodeForbidden) {
		t.Errorf("tags need job.manage_any: %v", err)
	}
	if _, err := s.SetTags(ctx, decider(), job, app, []string{"not-a-uuid"}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("bad id: %v", err)
	}
	a, b := "99999999-9999-4999-8999-999999999991", "99999999-9999-4999-8999-999999999992"
	if _, err := s.SetTags(ctx, decider(), job, app, []string{a, b, a}); err != nil || len(p.tagsSet) != 2 {
		t.Errorf("duplicates collapse: %v %v", err, p.tagsSet)
	}
	many := make([]string, MaxTagsPerApp+1)
	for i := range many {
		many[i] = a
	}
	if _, err := s.SetTags(ctx, decider(), job, app, many); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("too many: %v", err)
	}
}

func TestReorderRejectsDuplicatesAndBadIDs(t *testing.T) {
	s, _, _ := pipelineSvc(t, "")
	if err := s.ReorderStages(context.Background(), decider(), job, []string{stageApplied, stageApplied}); !errors.Is(err, ErrInvalidOrder) {
		t.Errorf("duplicate: %v", err)
	}
	if err := s.ReorderStages(context.Background(), decider(), job, []string{"x"}); !errors.Is(err, ErrInvalidOrder) {
		t.Errorf("bad id: %v", err)
	}
}

func TestStageNamesAreCleaned(t *testing.T) {
	if n, err := cleanName("  Phone   screen ", MaxStageName, "name"); err != nil || n != "Phone screen" {
		t.Errorf("%q %v", n, err)
	}
	if _, err := cleanName("   ", MaxStageName, "name"); err == nil {
		t.Error("blank")
	}
	long := make([]rune, MaxStageName+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := cleanName(string(long), MaxStageName, "name"); err == nil {
		t.Error("too long")
	}
}

func TestActivityCursor(t *testing.T) {
	s, _, _ := pipelineSvc(t, "")
	if _, err := s.Activity(context.Background(), decider(), job, app, "!!!", 10); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("bad cursor: %v", err)
	}
}

func TestRejectionReasonIsOptionalOnTheStatusEndpointButValidated(t *testing.T) {
	s, st, _ := pipelineSvc(t, "")
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusRejected}); err != nil {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusRejected, Reason: "nope"}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("unknown reason: %v", err)
	}
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusHired, Reason: "not_a_fit"}); !isCode(err, apperror.CodeValidationError) {
		t.Errorf("a reason with a hire: %v", err)
	}
	if _, err := s.SetStatus(context.Background(), decider(), job, app, StatusInput{Status: StatusRejected, Reason: "location"}); err != nil {
		t.Fatal(err)
	}
	if got := st.changes[len(st.changes)-1].Reason; got != "location" {
		t.Errorf("reason %q", got)
	}
}
