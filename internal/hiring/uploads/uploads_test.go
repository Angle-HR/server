package uploads_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/uploads"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// ---- fakes ----

type fakeStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    []string
}

func newStorage() *fakeStorage { return &fakeStorage{objects: map[string][]byte{}} }

func (f *fakeStorage) PresignPut(_ context.Context, key, ct string, ttl time.Duration) (uploads.PutTarget, error) {
	return uploads.PutTarget{URL: "https://r2.test/" + key + "?ttl=" + ttl.String(), Headers: map[string]string{"Content-Type": ct}}, nil
}
func (f *fakeStorage) Stat(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return 0, uploads.ErrObjectNotFound
	}
	return int64(len(b)), nil
}
func (f *fakeStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	if !ok {
		return nil, uploads.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeStorage) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, _ := io.ReadAll(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = b
	f.puts = append(f.puts, key)
	return nil
}
func (f *fakeStorage) Remove(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

type fakeStore struct {
	mu     sync.Mutex
	target *apply.Target
	rows   map[string]*uploads.Record
}

func newStore(t *apply.Target) *fakeStore {
	return &fakeStore{target: t, rows: map[string]*uploads.Record{}}
}

func (f *fakeStore) LoadForApply(context.Context, string, string) (*apply.Target, error) {
	return f.target, nil
}
func (f *fakeStore) InsertUpload(_ context.Context, _ string, r *uploads.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := *r
	f.rows[r.ID] = &c
	return nil
}
func (f *fakeStore) GetUpload(_ context.Context, _, id string) (*uploads.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return nil, hiringtypes.ErrNotFound
	}
	c := *r
	return &c, nil
}
func (f *fakeStore) MarkUploadClean(_ context.Context, _, id string, size int64, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	r.Status, r.Size, r.SHA256 = "clean", size, sha
	return nil
}
func (f *fakeStore) RejectUpload(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[id].Status, f.rows[id].Filename = "rejected", ""
	return nil
}

type dir struct {
	st *fakeStore
	sg *fakeStorage
}

func (d dir) Store(string) (uploads.Store, error)     { return d.st, nil }
func (d dir) Storage(string) (uploads.Storage, error) { return d.sg, nil }

type registry struct{}

func (registry) LookupJob(_ context.Context, id string) (hiringtypes.RegistryEntry, error) {
	if id != "jabc" {
		return hiringtypes.RegistryEntry{}, hiringtypes.ErrNotFound
	}
	return hiringtypes.RegistryEntry{Region: "uk", OrganizationID: "org-1"}, nil
}
func (registry) ListOpenJobs(context.Context, int) ([]public.OpenJob, error) { return nil, nil }

type fakeScanner struct {
	verdict uploads.Verdict
	err     error
	scanned [][]byte
}

func (s *fakeScanner) Scan(_ context.Context, r io.Reader) (uploads.Verdict, error) {
	b, _ := io.ReadAll(r)
	s.scanned = append(s.scanned, b)
	return s.verdict, s.err
}

type fakeLimiter struct{ n, max int }

func (l *fakeLimiter) Allow(context.Context, string, int, time.Duration) (bool, error) {
	l.n++
	return l.n <= l.max, nil
}

func target() *apply.Target {
	return &apply.Target{
		JobID: "job-1", Status: jobs.StatusPublished, ClosingDate: "2026-11-30", LawfulBasis: "legitimate_interests",
		Questions: []questions.FormQuestion{
			{ID: "cv", Type: "autofill_resume", Config: []byte(`{"max_mb":5,"accept":["pdf"]}`)},
			{ID: "photo", Type: "image_upload", Config: []byte(`{}`)},
			{ID: "name", Type: "short_text", Config: []byte(`{}`)},
			{ID: "health", Type: "file_upload", SpecialCategory: "health", Config: []byte(`{}`)},
		},
	}
}

type env struct {
	svc *uploads.Service
	st  *fakeStore
	sg  *fakeStorage
	sc  *fakeScanner
}

func setup(t *apply.Target) env {
	st, sg, sc := newStore(t), newStorage(), &fakeScanner{verdict: uploads.Verdict{Clean: true}}
	return env{
		svc: &uploads.Service{Registry: registry{}, Directory: dir{st, sg}, Scanner: sc, Now: func() time.Time { return now }},
		st:  st, sg: sg, sc: sc,
	}
}

func pdf(n int) []byte { return append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), n)...) }

func presignCV(t *testing.T, e env, size int64) *uploads.Presigned {
	t.Helper()
	p, err := e.svc.Presign(context.Background(), uploads.PresignInput{
		PublicID: "jabc", QuestionID: "cv", Filename: "My CV.pdf", ContentType: "application/pdf", SizeBytes: size, ClientIP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	return p
}

func claimReq(e env, token string) apply.ClaimRequest {
	return apply.ClaimRequest{
		Region: "uk", TenantID: "org-1", JobID: "job-1", QuestionID: "cv", Token: token, Question: &e.st.target.Questions[0],
	}
}

func fieldPath(err error) string {
	var ve *apply.ValidationError
	if errors.As(err, &ve) && len(ve.Fields) > 0 {
		return ve.Fields[0].Path
	}
	return ""
}

// ---- presign ----

func TestPresign(t *testing.T) {
	e := setup(target())
	p := presignCV(t, e, 1000)
	if p.Method != "PUT" || p.MaxBytes != 5<<20 || !strings.Contains(p.URL, "u/org-1/job-1/"+p.Token) {
		t.Errorf("presigned = %+v", p)
	}
	if p.Headers["Content-Type"] != "application/pdf" {
		t.Errorf("headers = %v", p.Headers)
	}
	if !p.ExpiresAt.Equal(now.Add(uploads.PresignTTL)) {
		t.Errorf("expires = %v", p.ExpiresAt)
	}
	row := e.st.rows[p.Token]
	if row == nil || row.Status != "pending" || row.QuestionID != "cv" || row.JobID != "job-1" || row.Filename != "My CV.pdf" {
		t.Fatalf("row = %+v", row)
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.Equal(now.Add(uploads.PendingTTL)) || !row.RetentionUntil.Equal(*row.ExpiresAt) {
		t.Errorf("a pending upload must carry an expiry and a matching retention date: %+v", row)
	}
	if row.LawfulBasis != "legitimate_interests" || row.CollectionPurpose == "" {
		t.Errorf("personal-data metadata missing: %+v", row)
	}
}

func TestPresignRefusals(t *testing.T) {
	in := func(mod func(*uploads.PresignInput)) uploads.PresignInput {
		p := uploads.PresignInput{PublicID: "jabc", QuestionID: "cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 1000, ClientIP: "1.1.1.1"}
		mod(&p)
		return p
	}
	cases := []struct {
		name string
		mod  func(*uploads.PresignInput)
		path string
	}{
		{"word file for a PDF-only CV", func(p *uploads.PresignInput) {
			p.Filename, p.ContentType = "cv.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		}, "content_type"},
		{"pdf name with an image type", func(p *uploads.PresignInput) { p.ContentType = "image/png" }, "content_type"},
		{"image name with a pdf type", func(p *uploads.PresignInput) { p.Filename = "cv.png" }, "content_type"},
		{"html", func(p *uploads.PresignInput) { p.Filename, p.ContentType = "cv.html", "text/html" }, "content_type"},
		{"too big", func(p *uploads.PresignInput) { p.SizeBytes = 5<<20 + 1 }, "size_bytes"},
		{"empty", func(p *uploads.PresignInput) { p.SizeBytes = 0 }, "size_bytes"},
		{"not a file question", func(p *uploads.PresignInput) { p.QuestionID = "name" }, "question_id"},
		{"unknown question", func(p *uploads.PresignInput) { p.QuestionID = "zzz" }, "question_id"},
		{"no name", func(p *uploads.PresignInput) { p.Filename = "  " }, "filename"},
	}
	for _, c := range cases {
		e := setup(target())
		_, err := e.svc.Presign(context.Background(), in(c.mod))
		if got := fieldPath(err); got != c.path {
			t.Errorf("%s: err = %v (field %q), want field %q", c.name, err, got, c.path)
		}
		if len(e.st.rows) != 0 {
			t.Errorf("%s: a row was written for a refused request", c.name)
		}
	}

	e := setup(target())
	if _, err := e.svc.Presign(context.Background(), in(func(p *uploads.PresignInput) { p.QuestionID = "health" })); !errors.Is(err, apply.ErrSensitiveUnavailable) {
		t.Errorf("special category file: %v", err)
	}
	if _, err := e.svc.Presign(context.Background(), in(func(p *uploads.PresignInput) { p.PublicID = "nope" })); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Errorf("unknown job: %v", err)
	}
}

func TestPresignJobStates(t *testing.T) {
	for status, want := range map[string]error{
		jobs.StatusPaused: apply.ErrNotAccepting, jobs.StatusClosed: apply.ErrNotAccepting, jobs.StatusExpired: apply.ErrNotAccepting,
		jobs.StatusArchived: apply.ErrGone, jobs.StatusDraft: apply.ErrNotFound,
	} {
		tg := target()
		tg.Status = status
		e := setup(tg)
		_, err := e.svc.Presign(context.Background(), uploads.PresignInput{
			PublicID: "jabc", QuestionID: "cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 10})
		if !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", status, err, want)
		}
	}
	tg := target()
	tg.ClosingDate = "2026-10-15" // yesterday
	if _, err := setup(tg).svc.Presign(context.Background(), uploads.PresignInput{
		PublicID: "jabc", QuestionID: "cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 10}); !errors.Is(err, apply.ErrNotAccepting) {
		t.Errorf("past closing date: %v", err)
	}
}

func TestPresignNeedsScannerAndLimits(t *testing.T) {
	e := setup(target())
	e.svc.Scanner = nil
	if _, err := e.svc.Presign(context.Background(), uploads.PresignInput{PublicID: "jabc", QuestionID: "cv"}); !errors.Is(err, apply.ErrFilesUnavailable) {
		t.Errorf("no scanner: %v", err)
	}

	e = setup(target())
	e.svc.Limiter = &fakeLimiter{max: 1}
	presignCV(t, e, 10)
	_, err := e.svc.Presign(context.Background(), uploads.PresignInput{
		PublicID: "jabc", QuestionID: "cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 10})
	if !errors.Is(err, apply.ErrRateLimited) {
		t.Errorf("second request: %v", err)
	}
}

func TestPlanLimitWins(t *testing.T) {
	tg := target()
	tg.Questions[0].Config = []byte(`{"max_mb":10,"accept":["pdf"]}`)
	e := setup(tg)
	e.svc.PlanMaxMB = func(context.Context, string) int { return 5 }
	_, err := e.svc.Presign(context.Background(), uploads.PresignInput{
		PublicID: "jabc", QuestionID: "cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 6 << 20})
	if fieldPath(err) != "size_bytes" {
		t.Errorf("free plan must cap at 5 MB: %v", err)
	}
	e.svc.PlanMaxMB = func(context.Context, string) int { return 10 }
	if _, err = e.svc.Presign(context.Background(), uploads.PresignInput{
		PublicID: "jabc", QuestionID: "cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 6 << 20}); err != nil {
		t.Errorf("paid plan: %v", err)
	}
}

func TestDefaultsWhenConfigIsEmpty(t *testing.T) {
	e := setup(target())
	// image_upload with {} accepts png, jpeg and webp up to 5 MB
	for _, c := range []struct{ name, ct string }{{"a.png", "image/png"}, {"a.jpg", "image/jpeg"}, {"a.webp", "image/webp"}} {
		if _, err := e.svc.Presign(context.Background(), uploads.PresignInput{
			PublicID: "jabc", QuestionID: "photo", Filename: c.name, ContentType: c.ct, SizeBytes: 100}); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := e.svc.Presign(context.Background(), uploads.PresignInput{
		PublicID: "jabc", QuestionID: "photo", Filename: "a.svg", ContentType: "image/svg+xml", SizeBytes: 100}); err == nil {
		t.Error("svg must never be accepted")
	}
}

// ---- claim ----

func put(e env, p *uploads.Presigned, b []byte) {
	e.sg.objects[uploads.StagingKey("org-1", "job-1", p.Token)] = b
}

func TestClaimCleanFile(t *testing.T) {
	e := setup(target())
	p := presignCV(t, e, 2000)
	body := pdf(2000 - 9)
	put(e, p, body)

	f, err := e.svc.Claim(context.Background(), claimReq(e, p.Token))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if f.ID != p.Token || f.QuestionID != "cv" || f.SizeBytes != int64(len(body)) || len(f.SHA256) != 64 {
		t.Errorf("file = %+v", f)
	}
	if !bytes.Equal(e.sg.objects[uploads.FinalKey("org-1", "job-1", p.Token)], body) {
		t.Error("the scanned bytes must be stored under the final key")
	}
	if _, still := e.sg.objects[uploads.StagingKey("org-1", "job-1", p.Token)]; still {
		t.Error("the staging object must be removed, so a later overwrite through the presigned URL changes nothing")
	}
	if len(e.sc.scanned) != 1 || !bytes.Equal(e.sc.scanned[0], body) {
		t.Error("the file must be scanned once, with the same bytes that are stored")
	}
	if r := e.st.rows[p.Token]; r.Status != "clean" || r.Size != int64(len(body)) || r.SHA256 != f.SHA256 {
		t.Errorf("row = %+v", r)
	}

	// A retried submit claims the same token again: no second scan, same file.
	f2, err := e.svc.Claim(context.Background(), claimReq(e, p.Token))
	if err != nil || f2 != f {
		t.Errorf("repeat claim = %+v, %v", f2, err)
	}
	if len(e.sc.scanned) != 1 {
		t.Error("a repeat claim must not scan again")
	}
}

func TestClaimRefusals(t *testing.T) {
	type tc struct {
		name  string
		setup func(e env, p *uploads.Presigned)
		req   func(r *apply.ClaimRequest)
	}
	tooBig := bytes.Repeat([]byte("x"), 5<<20+1)
	cases := []tc{
		{"nothing uploaded", func(e env, p *uploads.Presigned) {}, nil},
		{"unknown token", func(e env, p *uploads.Presigned) { put(e, p, pdf(100)) }, func(r *apply.ClaimRequest) { r.Token = "6f1c7d52-1b0a-4a55-9d1e-0f4c5b8a2c31" }},
		{"not a token", func(e env, p *uploads.Presigned) { put(e, p, pdf(100)) }, func(r *apply.ClaimRequest) { r.Token = "uploads/abc.pdf" }},
		{"another question", func(e env, p *uploads.Presigned) { put(e, p, pdf(100)) }, func(r *apply.ClaimRequest) { r.QuestionID = "photo" }},
		{"another job", func(e env, p *uploads.Presigned) { put(e, p, pdf(100)) }, func(r *apply.ClaimRequest) { r.JobID = "job-2" }},
		{"too big", func(e env, p *uploads.Presigned) { put(e, p, tooBig) }, nil},
		{"empty object", func(e env, p *uploads.Presigned) { put(e, p, nil) }, nil},
		{"not a pdf", func(e env, p *uploads.Presigned) { put(e, p, []byte("<html><script>alert(1)</script></html>")) }, nil},
		{"png renamed to pdf", func(e env, p *uploads.Presigned) { put(e, p, []byte("\x89PNG\r\n\x1a\nxxxx")) }, nil},
		{"size differs from declared", func(e env, p *uploads.Presigned) { put(e, p, pdf(50)) }, nil},
	}
	for _, c := range cases {
		e := setup(target())
		p := presignCV(t, e, 100)
		c.setup(e, p)
		req := claimReq(e, p.Token)
		if c.req != nil {
			c.req(&req)
		}
		if _, err := e.svc.Claim(context.Background(), req); !errors.Is(err, apply.ErrFileRejected) {
			t.Errorf("%s: err = %v, want rejected", c.name, err)
		}
		if len(e.sg.puts) != 0 {
			t.Errorf("%s: a refused file reached the final key", c.name)
		}
	}
}

func TestClaimExpiredAndRejectedTokens(t *testing.T) {
	e := setup(target())
	p := presignCV(t, e, 100)
	put(e, p, pdf(100-9))
	e.svc.Now = func() time.Time { return now.Add(uploads.PendingTTL + time.Second) }
	if _, err := e.svc.Claim(context.Background(), claimReq(e, p.Token)); !errors.Is(err, apply.ErrFileRejected) {
		t.Errorf("expired token: %v", err)
	}

	e = setup(target())
	e.sc.verdict = uploads.Verdict{Signature: "Eicar-Test-Signature"}
	p = presignCV(t, e, 100)
	put(e, p, pdf(100-9))
	if _, err := e.svc.Claim(context.Background(), claimReq(e, p.Token)); !errors.Is(err, apply.ErrFileRejected) {
		t.Fatalf("infected file: %v", err)
	}
	if _, ok := e.sg.objects[uploads.StagingKey("org-1", "job-1", p.Token)]; ok {
		t.Error("an infected object must be deleted")
	}
	if len(e.sg.puts) != 0 {
		t.Error("an infected file reached the final key")
	}
	if r := e.st.rows[p.Token]; r.Status != "rejected" || r.Filename != "" {
		t.Errorf("row = %+v", r)
	}
	e.sc.verdict = uploads.Verdict{Clean: true}
	put(e, p, pdf(100-9)) // the candidate uploads again to the same URL
	if _, err := e.svc.Claim(context.Background(), claimReq(e, p.Token)); !errors.Is(err, apply.ErrFileRejected) {
		t.Errorf("a rejected token must stay rejected: %v", err)
	}
}

func TestClaimFailsClosedWhenScannerIsDown(t *testing.T) {
	e := setup(target())
	e.sc.err = uploads.ErrScannerUnavailable
	p := presignCV(t, e, 100)
	put(e, p, pdf(100-9))
	_, err := e.svc.Claim(context.Background(), claimReq(e, p.Token))
	if !errors.Is(err, apply.ErrFilesUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(e.sg.puts) != 0 || e.st.rows[p.Token].Status != "pending" {
		t.Error("an unscanned file must not be kept, and the token must stay usable for a retry")
	}
	if _, ok := e.sg.objects[uploads.StagingKey("org-1", "job-1", p.Token)]; !ok {
		t.Error("the staging object must stay for the retry")
	}
	e.sc.err = nil
	e.sc.verdict = uploads.Verdict{Clean: true}
	if _, err = e.svc.Claim(context.Background(), claimReq(e, p.Token)); err != nil {
		t.Errorf("retry after the scanner is back: %v", err)
	}

	e.svc.Scanner = nil
	if _, err = e.svc.Claim(context.Background(), claimReq(e, p.Token)); !errors.Is(err, apply.ErrFilesUnavailable) {
		t.Errorf("no scanner configured: %v", err)
	}
}

// ---- sweeper ----

type sweepStore struct {
	expired map[string][]uploads.Expired
	deleted []string
	failID  string
}

func (s *sweepStore) ExpiredUploads(_ context.Context, tenantID string, _ time.Time, limit int) ([]uploads.Expired, error) {
	list := s.expired[tenantID]
	if len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}
func (s *sweepStore) DeleteUpload(_ context.Context, tenantID, id string) error {
	if id == s.failID {
		return errors.New("db down")
	}
	s.deleted = append(s.deleted, id)
	rest := s.expired[tenantID][:0:0]
	for _, e := range s.expired[tenantID] {
		if e.ID != id {
			rest = append(rest, e)
		}
	}
	s.expired[tenantID] = rest
	return nil
}

func TestSweeperDeletesObjectsThenRows(t *testing.T) {
	sg := newStorage()
	sg.objects[uploads.StagingKey("org-1", "job-1", "a")] = []byte("x")
	sg.objects[uploads.FinalKey("org-1", "job-1", "a")] = []byte("x")
	sg.objects[uploads.StagingKey("org-1", "job-1", "keep")] = []byte("x")
	st := &sweepStore{expired: map[string][]uploads.Expired{
		"org-1": {{ID: "a", JobID: "job-1", TenantID: "org-1"}, {ID: "b", JobID: "job-1", TenantID: "org-1"}},
		"org-2": {{ID: "c", JobID: "job-9", TenantID: "org-2"}},
	}}
	w := &uploads.Sweeper{
		Tenants: func(context.Context) ([]string, error) { return []string{"org-1", "org-2"}, nil },
		Store:   st, Storage: sg, Now: func() time.Time { return now },
	}
	res, err := w.Run(context.Background())
	if err != nil || res.Deleted != 3 || res.Companies != 2 || res.Errors != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(sg.objects) != 1 || sg.objects[uploads.StagingKey("org-1", "job-1", "keep")] == nil {
		t.Errorf("objects left = %v", sg.objects)
	}
}

func TestSweeperKeepsGoingAfterAFailure(t *testing.T) {
	st := &sweepStore{failID: "a", expired: map[string][]uploads.Expired{
		"org-1": {{ID: "a", JobID: "j", TenantID: "org-1"}, {ID: "b", JobID: "j", TenantID: "org-1"}},
	}}
	w := &uploads.Sweeper{
		Tenants: func(context.Context) ([]string, error) { return []string{"org-1"}, nil },
		Store:   st, Storage: newStorage(),
	}
	res, err := w.Run(context.Background())
	if err == nil || res.Errors != 1 || res.Deleted != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

// ---- ClamAV ----

// clamd answers a INSTREAM request with reply and reports what it received.
func fakeClamd(t *testing.T, reply string, got chan<- []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		cmd := make([]byte, len("zINSTREAM\x00"))
		if _, err = io.ReadFull(c, cmd); err != nil || string(cmd) != "zINSTREAM\x00" {
			return
		}
		var data []byte
		for {
			var n [4]byte
			if _, err = io.ReadFull(c, n[:]); err != nil {
				return
			}
			size := binary.BigEndian.Uint32(n[:])
			if size == 0 {
				break
			}
			chunk := make([]byte, size)
			if _, err = io.ReadFull(c, chunk); err != nil {
				return
			}
			data = append(data, chunk...)
		}
		got <- data
		_, _ = c.Write([]byte(reply))
	}()
	return ln.Addr().String()
}

func TestClamAV(t *testing.T) {
	big := bytes.Repeat([]byte("abcdefgh"), 20000) // 160 KB: several chunks
	for _, c := range []struct {
		reply string
		clean bool
		sig   string
		fail  bool
	}{
		{"stream: OK\x00", true, "", false},
		{"stream: Eicar-Test-Signature FOUND\x00", false, "Eicar-Test-Signature", false},
		{"INSTREAM size limit exceeded. ERROR\x00", false, "", true},
		{"", false, "", true},
	} {
		got := make(chan []byte, 1)
		av := &uploads.ClamAV{Addr: fakeClamd(t, c.reply, got), Timeout: 5 * time.Second}
		v, err := av.Scan(context.Background(), bytes.NewReader(big))
		if (err != nil) != c.fail || v.Clean != c.clean || v.Signature != c.sig {
			t.Errorf("reply %q: verdict %+v err %v", c.reply, v, err)
		}
		if c.fail && !errors.Is(err, uploads.ErrScannerUnavailable) {
			t.Errorf("reply %q: a scan that gave no answer must be ErrScannerUnavailable, got %v", c.reply, err)
		}
		select {
		case data := <-got:
			if !bytes.Equal(data, big) {
				t.Errorf("reply %q: clamd received %d bytes, want %d", c.reply, len(data), len(big))
			}
		case <-time.After(2 * time.Second):
			t.Errorf("reply %q: clamd received nothing", c.reply)
		}
	}
}

func TestClamAVUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens there now
	av := &uploads.ClamAV{Addr: addr, Timeout: 2 * time.Second}
	_, err := av.Scan(context.Background(), strings.NewReader("x"))
	if !errors.Is(err, uploads.ErrScannerUnavailable) {
		t.Errorf("err = %v", err)
	}
}
