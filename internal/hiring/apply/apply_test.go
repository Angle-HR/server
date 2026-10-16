package apply_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
)

type fakeRegistry struct{}

func (fakeRegistry) LookupJob(_ context.Context, id string) (hiringtypes.RegistryEntry, error) {
	if id != "jabcdefghijkl" {
		return hiringtypes.RegistryEntry{}, apply.ErrNotFound
	}
	return hiringtypes.RegistryEntry{PublicID: id, Region: "uk", OrganizationID: "org-1"}, nil
}

func (fakeRegistry) ListOpenJobs(context.Context, int) ([]public.OpenJob, error) { return nil, nil }

type fakeStore struct {
	mu      sync.Mutex
	target  *apply.Target
	saved   []*apply.NewApplication
	emails  map[string]bool
	keys    map[string]bool
	loadErr error
	// insertErr is returned by InsertApplication.
	insertErr error
}

func (f *fakeStore) LoadForApply(context.Context, string, string) (*apply.Target, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.target, nil
}

func (f *fakeStore) InsertApplication(_ context.Context, _ string, a *apply.NewApplication) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return false, f.insertErr
	}
	if f.emails == nil {
		f.emails, f.keys = map[string]bool{}, map[string]bool{}
	}
	if f.emails[a.Email] || (a.IdempotencyKey != "" && f.keys[a.IdempotencyKey]) {
		return false, nil
	}
	f.emails[a.Email] = true
	if a.IdempotencyKey != "" {
		f.keys[a.IdempotencyKey] = true
	}
	f.saved = append(f.saved, a)
	return true, nil
}

type fakeDir struct{ s apply.Store }

func (d fakeDir) Store(string) (apply.Store, error) { return d.s, nil }

type countLimiter struct {
	counts map[string]int
	err    error
}

func (l *countLimiter) Allow(_ context.Context, key string, limit int, _ time.Duration) (bool, error) {
	if l.err != nil {
		return false, l.err
	}
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.counts[key]++
	return l.counts[key] <= limit, nil
}

// fakeUploads accepts the tokens in ok, fails with err for every token when err is set, and records the
// requests it saw.
type fakeUploads struct {
	ok   map[string]bool
	err  error
	seen *[]apply.ClaimRequest
}

func (u fakeUploads) Claim(_ context.Context, req apply.ClaimRequest) (apply.File, error) {
	if u.seen != nil {
		*u.seen = append(*u.seen, req)
	}
	if u.err != nil {
		return apply.File{}, u.err
	}
	if u.ok[req.Token] {
		return apply.File{ID: req.Token, QuestionID: req.QuestionID, SizeBytes: 10, SHA256: "ab"}, nil
	}
	return apply.File{}, apply.ErrFileRejected
}

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func options() json.RawMessage {
	return json.RawMessage(`{"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}`)
}

func target() *apply.Target {
	return &apply.Target{
		JobID: "job-1", JobCode: "JB-1", Status: jobs.StatusPublished, ClosingDate: "2026-11-30",
		LawfulBasis: "legitimate_interests", RetentionMonths: 6, Markets: []string{"UK", "EU"},
		FormID: "form-1", FormVersion: 2,
		Questions: []questions.FormQuestion{
			{ID: "name", Section: "personal_information", Type: "short_text", Label: "Full name", Required: true, SystemKey: "full_name"},
			{ID: "mail", Section: "personal_information", Type: "email", Label: "Email", Required: true, SystemKey: "email"},
			{ID: "rtw", Section: "eligibility", Type: "single_choice", Label: "Right to work?", Required: true, Config: options(),
				Knockout: json.RawMessage(`{"option_ids":["no"],"reason":"No right to work"}`)},
			{ID: "years", Section: "screening", Type: "number", Label: "Years", Config: json.RawMessage(`{}`)},
		},
	}
}

func service(st *fakeStore, lim apply.Limiter) *apply.Service {
	return &apply.Service{
		Registry: fakeRegistry{}, Directory: fakeDir{s: st}, Limiter: lim,
		Now: func() time.Time { return now },
	}
}

func input() apply.Input {
	return apply.Input{
		PublicID: "jabcdefghijkl", CandidateCountry: "gb", NoticeAcknowledged: true, ClientIP: "203.0.113.5",
		Answers: map[string]json.RawMessage{
			"name": json.RawMessage(`"Ada Lovelace"`), "mail": json.RawMessage(`"Ada@Example.com "`),
			"rtw": json.RawMessage(`"yes"`),
		},
	}
}

func fields(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *apply.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a validation error, got %v", err)
	}
	m := map[string]string{}
	for _, f := range ve.Fields {
		m[f.Path] = f.Message
	}
	return m
}

func TestSubmitStoresAValidApplication(t *testing.T) {
	st := &fakeStore{target: target()}
	rec, err := service(st, nil).Submit(context.Background(), input())
	if err != nil || !rec.Received {
		t.Fatalf("submit: %+v %v", rec, err)
	}
	if len(st.saved) != 1 {
		t.Fatalf("saved %d applications", len(st.saved))
	}
	a := st.saved[0]
	if a.Email != "ada@example.com" || a.Name != "Ada Lovelace" || a.Country != "GB" || a.Market != "UK" {
		t.Errorf("candidate fields: %+v", a)
	}
	if a.Status != apply.StatusReceived || len(a.Flags) != 0 || a.Source != "direct" {
		t.Errorf("status/flags/source: %q %v %q", a.Status, a.Flags, a.Source)
	}
	if a.FormID != "form-1" || a.FormVersion != 2 || a.JobID != "job-1" {
		t.Errorf("job/form: %+v", a)
	}
	if a.NoticeVersion != public.NoticeVersion || a.NoticeAcknowledgedAt.IsZero() || a.ConsentAt != nil {
		t.Errorf("notice/consent: %+v", a)
	}
	// Closing date 30 Nov is the last day to apply: retention counts from the day after, plus 6 months.
	if want := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC); !a.RetentionUntil.Equal(want) {
		t.Errorf("retention_until = %v, want %v", a.RetentionUntil, want)
	}
	if a.LawfulBasis != "legitimate_interests" || a.CollectionPurpose == "" {
		t.Errorf("personal data metadata: %+v", a)
	}
}

func TestKnockoutAndRulesFlagButNeverReject(t *testing.T) {
	tg := target()
	tg.Rules = []screening.Rule{{ID: "r1", QuestionID: "years", Operator: "lt", Value: json.RawMessage(`2`), Reason: "Under 2 years"}}
	st := &fakeStore{target: tg}
	in := input()
	in.Answers["rtw"] = json.RawMessage(`"no"`)
	in.Answers["years"] = json.RawMessage(`1`)
	if _, err := service(st, nil).Submit(context.Background(), in); err != nil {
		t.Fatalf("a flagged application is still accepted: %v", err)
	}
	a := st.saved[0]
	if a.Status != apply.StatusFlagged || len(a.Flags) != 2 {
		t.Fatalf("status %q flags %+v", a.Status, a.Flags)
	}
	reasons := map[string]bool{a.Flags[0].Reason: true, a.Flags[1].Reason: true}
	if !reasons["No right to work"] || !reasons["Under 2 years"] {
		t.Errorf("flag reasons: %+v", a.Flags)
	}
}

func TestUnansweredKnockoutQuestionDoesNotFlag(t *testing.T) {
	tg := target()
	tg.Questions[2].Required = false
	st := &fakeStore{target: tg}
	in := input()
	delete(in.Answers, "rtw")
	if _, err := service(st, nil).Submit(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if st.saved[0].Status != apply.StatusReceived {
		t.Errorf("a blank answer must not flag anyone: %+v", st.saved[0])
	}
}

func TestValidationErrorsPointAtFields(t *testing.T) {
	st := &fakeStore{target: target()}
	svc := service(st, nil)

	in := input()
	delete(in.Answers, "rtw")                              // required, missing
	in.Answers["mail"] = json.RawMessage(`"not-an-email"`) // invalid
	in.Answers["ghost"] = json.RawMessage(`"x"`)           // not on the form
	in.CandidateCountry = "BR"                             // no market
	in.NoticeAcknowledged = false                          // must be ticked
	f := fields(t, func() error { _, err := svc.Submit(context.Background(), in); return err }())
	for _, path := range []string{"answers.rtw", "answers.mail", "answers.ghost", "candidate_country", "privacy_notice_acknowledged"} {
		if f[path] == "" {
			t.Errorf("no error for %s (got %v)", path, f)
		}
	}
	if len(st.saved) != 0 {
		t.Error("nothing is saved when validation fails")
	}
}

func TestCountryMustBelongToAMarketOfTheJob(t *testing.T) {
	st := &fakeStore{target: target()}
	svc := service(st, nil)
	for country, ok := range map[string]bool{"GB": true, "FR": true, "DE": true, "US": false, "NG": false, "": false, "GBR": false} {
		in := input()
		in.CandidateCountry = country
		in.Answers["mail"] = json.RawMessage(`"` + country + `@example.com"`)
		_, err := svc.Submit(context.Background(), in)
		if ok && err != nil {
			t.Errorf("%q should be accepted: %v", country, err)
		}
		if !ok {
			if f := fields(t, err); f["candidate_country"] == "" {
				t.Errorf("%q: %v", country, f)
			}
		}
	}
}

func TestConsentRequiredWhenLawfulBasisIsConsent(t *testing.T) {
	tg := target()
	tg.LawfulBasis = "consent"
	st := &fakeStore{target: tg}
	_, err := service(st, nil).Submit(context.Background(), input())
	if f := fields(t, err); f["consent"] == "" {
		t.Fatalf("consent must be required: %v", f)
	}
	in := input()
	in.Consent = true
	if _, err = service(st, nil).Submit(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if st.saved[0].ConsentAt == nil || st.saved[0].LawfulBasis != "consent" {
		t.Errorf("consent not recorded: %+v", st.saved[0])
	}
}

func TestOnlyOpenJobsAcceptApplications(t *testing.T) {
	cases := []struct {
		status, closing string
		want            error
	}{
		{jobs.StatusPaused, "2026-11-30", apply.ErrNotAccepting},
		{jobs.StatusClosed, "", apply.ErrNotAccepting},
		{jobs.StatusExpired, "", apply.ErrNotAccepting},
		{jobs.StatusPublished, "2026-10-15", apply.ErrNotAccepting}, // closing date passed
		{jobs.StatusArchived, "", apply.ErrGone},
		{jobs.StatusDraft, "", apply.ErrNotFound},
	}
	for _, c := range cases {
		tg := target()
		tg.Status, tg.ClosingDate = c.status, c.closing
		st := &fakeStore{target: tg}
		if _, err := service(st, nil).Submit(context.Background(), input()); !errors.Is(err, c.want) {
			t.Errorf("%s/%s: got %v, want %v", c.status, c.closing, err, c.want)
		}
		if len(st.saved) != 0 {
			t.Errorf("%s: an application was saved", c.status)
		}
	}
	// The closing date itself is still open.
	tg := target()
	tg.ClosingDate = "2026-10-16"
	if _, err := service(&fakeStore{target: tg}, nil).Submit(context.Background(), input()); err != nil {
		t.Errorf("the last day to apply must be open: %v", err)
	}
}

func TestUnknownJob(t *testing.T) {
	in := input()
	in.PublicID = "jnosuchjob123"
	if _, err := service(&fakeStore{target: target()}, nil).Submit(context.Background(), in); !errors.Is(err, apply.ErrNotFound) {
		t.Errorf("got %v", err)
	}
}

func TestRepeatApplicationLooksIdenticalToTheCandidate(t *testing.T) {
	st := &fakeStore{target: target()}
	svc := service(st, nil)
	first, err1 := svc.Submit(context.Background(), input())
	second, err2 := svc.Submit(context.Background(), input())
	if err1 != nil || err2 != nil || *first != *second {
		t.Fatalf("repeat must look like the first: %+v %v / %+v %v", first, err1, second, err2)
	}
	if len(st.saved) != 1 {
		t.Errorf("saved %d, want 1", len(st.saved))
	}
}

func TestIdempotencyKeyIsChecked(t *testing.T) {
	svc := service(&fakeStore{target: target()}, nil)
	in := input()
	in.IdempotencyKey = "short"
	if f := fields(t, func() error { _, err := svc.Submit(context.Background(), in); return err }()); f["idempotency_key"] == "" {
		t.Errorf("a malformed key must be rejected: %v", f)
	}
}

func TestSourceIsLimitedToKnownChannels(t *testing.T) {
	st := &fakeStore{target: target()}
	svc := service(st, nil)
	in := input()
	in.Source = "LinkedIn"
	if _, err := svc.Submit(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in2 := input()
	in2.Answers["mail"] = json.RawMessage(`"b@example.com"`)
	in2.Source = "<script>"
	if _, err := svc.Submit(context.Background(), in2); err != nil {
		t.Fatal(err)
	}
	if st.saved[0].Source != "linkedin" || st.saved[1].Source != "direct" {
		t.Errorf("sources: %q %q", st.saved[0].Source, st.saved[1].Source)
	}
}

func TestRateLimits(t *testing.T) {
	st := &fakeStore{target: target()}
	lim := &countLimiter{}
	svc := service(st, lim)
	// The per-email limit stops retries for one email on one job.
	var err error
	for i := 0; i < apply.PerEmailPerJobPerHour+1; i++ {
		_, err = svc.Submit(context.Background(), input())
	}
	if !errors.Is(err, apply.ErrRateLimited) {
		t.Errorf("per-email limit: %v", err)
	}
	// The per-address limit stops floods before any database work.
	lim2 := &countLimiter{}
	svc2 := service(&fakeStore{target: target(), loadErr: errors.New("must not be reached")}, lim2)
	var err2 error
	for i := 0; i < apply.PerIPPerHour+1; i++ {
		_, err2 = svc2.Submit(context.Background(), input())
	}
	if !errors.Is(err2, apply.ErrRateLimited) {
		t.Errorf("per-IP limit: %v", err2)
	}
}

func TestLimiterOutageDoesNotBlockApplications(t *testing.T) {
	st := &fakeStore{target: target()}
	if _, err := service(st, &countLimiter{err: errors.New("redis down")}).Submit(context.Background(), input()); err != nil {
		t.Fatalf("a limiter outage must let the application through: %v", err)
	}
}

func TestSpecialCategoryAnswersAreNeverStoredYet(t *testing.T) {
	tg := target()
	tg.Questions = append(tg.Questions, questions.FormQuestion{
		ID: "health", Section: "screening", Type: "single_choice", Label: "Disability?", Config: options(),
		SpecialCategory: "health_disability",
	})
	st := &fakeStore{target: tg}
	in := input()
	in.Answers["health"] = json.RawMessage(`"yes"`)
	if _, err := service(st, nil).Submit(context.Background(), in); !errors.Is(err, apply.ErrSensitiveUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(st.saved) != 0 {
		t.Fatal("special category answers must never reach the standard database")
	}
	// An optional sensitive question left blank does not block the application.
	delete(in.Answers, "health")
	if _, err := service(st, nil).Submit(context.Background(), in); err != nil {
		t.Errorf("blank optional special category question: %v", err)
	}
	if _, has := st.saved[0].Answers["health"]; has {
		t.Error("a special category answer slipped into the saved answers")
	}
}

func TestFileQuestionsNeedTheUploadStep(t *testing.T) {
	tg := target()
	tg.Questions = append(tg.Questions, questions.FormQuestion{
		ID: "cv", Section: "profile", Type: "autofill_resume", Label: "CV", Required: true, SystemKey: "cv", Config: json.RawMessage(`{}`),
	})
	in := input()
	in.Answers["cv"] = json.RawMessage(`"uploads/abc.pdf"`)

	st := &fakeStore{target: tg}
	if _, err := service(st, nil).Submit(context.Background(), in); !errors.Is(err, apply.ErrFilesUnavailable) {
		t.Fatalf("without the upload step: %v", err)
	}

	svc := service(st, nil)
	var seen []apply.ClaimRequest
	svc.Uploads = fakeUploads{ok: map[string]bool{"uploads/abc.pdf": true}, seen: &seen}
	if _, err := svc.Submit(context.Background(), in); err != nil {
		t.Fatalf("with a valid upload key: %v", err)
	}
	if len(seen) != 1 || seen[0].QuestionID != "cv" || seen[0].Token != "uploads/abc.pdf" || seen[0].JobID != "job-1" ||
		seen[0].Question == nil || seen[0].Question.ID != "cv" {
		t.Errorf("claim request = %+v", seen)
	}
	if got := st.saved[len(st.saved)-1].Files; len(got) != 1 || got[0].ID != "uploads/abc.pdf" || got[0].QuestionID != "cv" {
		t.Errorf("saved files = %+v", got)
	}
	st2 := &fakeStore{target: tg}
	svc2 := service(st2, nil)
	svc2.Uploads = fakeUploads{ok: map[string]bool{}}
	if f := fields(t, func() error { _, err := svc2.Submit(context.Background(), in); return err }()); f["answers.cv"] == "" {
		t.Errorf("a key the server did not issue must be rejected: %v", f)
	}
}

func TestUploadProblems(t *testing.T) {
	tg := target()
	tg.Questions = append(tg.Questions, questions.FormQuestion{
		ID: "cv", Section: "profile", Type: "autofill_resume", Label: "CV", Required: true, SystemKey: "cv", Config: json.RawMessage(`{}`),
	})
	in := input()
	in.Answers["cv"] = json.RawMessage(`"tok"`)

	// A scanner that cannot be reached must not become "clean" or a validation message: the application fails
	// with the unavailable error and nothing is saved.
	st := &fakeStore{target: tg}
	svc := service(st, nil)
	svc.Uploads = fakeUploads{err: apply.ErrFilesUnavailable}
	if _, err := svc.Submit(context.Background(), in); !errors.Is(err, apply.ErrFilesUnavailable) {
		t.Fatalf("scanner down: %v", err)
	}
	if len(st.saved) != 0 {
		t.Error("an application was saved although its file could not be checked")
	}

	// Any other failure of the upload step is an internal error, not the candidate's mistake.
	svc.Uploads = fakeUploads{err: errors.New("database down")}
	_, err := svc.Submit(context.Background(), in)
	var ve *apply.ValidationError
	if err == nil || errors.As(err, &ve) || errors.Is(err, apply.ErrFilesUnavailable) {
		t.Fatalf("internal failure surfaced as %v", err)
	}

	// A file used or expired between the claim and the save is a mistake of the candidate's, on that question.
	st3 := &fakeStore{target: tg, insertErr: &apply.FileError{QuestionID: "cv"}}
	svc3 := service(st3, nil)
	svc3.Uploads = fakeUploads{ok: map[string]bool{"tok": true}}
	if f := fields(t, func() error { _, err := svc3.Submit(context.Background(), in); return err }()); f["answers.cv"] == "" {
		t.Errorf("file lost before the save: %v", f)
	}

	// An optional file question left empty needs no upload step.
	tg2 := target()
	tg2.Questions = append(tg2.Questions, questions.FormQuestion{
		ID: "extra", Section: "profile", Type: "file_upload", Label: "Portfolio", Config: json.RawMessage(`{}`),
	})
	if _, err := service(&fakeStore{target: tg2}, nil).Submit(context.Background(), input()); err != nil {
		t.Errorf("blank optional file question: %v", err)
	}
}

func TestMarketFor(t *testing.T) {
	cases := map[string]string{"GB": "UK", "gb": "UK", "FR": "EU", "IE": "EU", "SE": "EU", "US": "US", "IN": "IN", "NG": "NG", "KE": "KE", "ZA": "ZA"}
	for c, want := range cases {
		if got, ok := apply.MarketFor(c); !ok || got != want {
			t.Errorf("MarketFor(%q) = %q,%v want %q", c, got, ok, want)
		}
	}
	for _, c := range []string{"BR", "UK", "", "CH", "NO"} { // UK is not an ISO code; CH and NO are outside the EU
		if got, ok := apply.MarketFor(c); ok {
			t.Errorf("MarketFor(%q) = %q, want no market", c, got)
		}
	}
}

func TestRetentionUntil(t *testing.T) {
	n := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if got, want := apply.RetentionUntil(n, "", 6), time.Date(2027, 4, 16, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("no closing date: %v want %v", got, want)
	}
	if got, want := apply.RetentionUntil(n, "2026-12-31", 12), time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("closing date in the future: %v want %v", got, want)
	}
	if got, want := apply.RetentionUntil(n, "2026-01-01", 3), time.Date(2027, 1, 16, 12, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("closing date in the past starts from now: %v want %v", got, want)
	}
	if got := apply.RetentionUntil(n, "", 0); !got.Equal(time.Date(2027, 4, 16, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("months default to %d: %v", jobs.DefaultRetentionMonths, got)
	}
}

func TestMultiChoiceKnockoutMatchAll(t *testing.T) {
	tg := target()
	tg.Questions = append(tg.Questions, questions.FormQuestion{
		ID: "tools", Section: "screening", Type: "checkbox", Label: "Tools", Config: json.RawMessage(
			`{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"},{"id":"c","label":"C"}]}`),
		Knockout: json.RawMessage(`{"option_ids":["a","b"],"match":"all","reason":"Uses both"}`),
	})
	st := &fakeStore{target: tg}
	svc := service(st, nil)
	in := input()
	in.Answers["tools"] = json.RawMessage(`["a","c"]`)
	if _, err := svc.Submit(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if st.saved[0].Status != apply.StatusReceived {
		t.Errorf("only one of two required options picked: %+v", st.saved[0].Flags)
	}
	in2 := input()
	in2.Answers["mail"] = json.RawMessage(`"c@example.com"`)
	in2.Answers["tools"] = json.RawMessage(`["a","b"]`)
	if _, err := svc.Submit(context.Background(), in2); err != nil {
		t.Fatal(err)
	}
	if st.saved[1].Status != apply.StatusFlagged || st.saved[1].Flags[0].Reason != "Uses both" {
		t.Errorf("both picked should flag: %+v", st.saved[1])
	}
}
