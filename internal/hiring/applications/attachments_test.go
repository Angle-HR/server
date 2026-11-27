package applications

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/uploads"
)

type fakeAttach struct {
	AttachmentStore
	added []*Attachment
}

func (f *fakeAttach) AddAttachment(_ context.Context, _, _, _, _ string, a *Attachment) error {
	f.added = append(f.added, a)
	return nil
}

type fakeScan struct {
	clean bool
	err   error
}

func (f fakeScan) Scan(context.Context, io.Reader) (uploads.Verdict, error) {
	return uploads.Verdict{Clean: f.clean}, f.err
}

type fakeDocs struct {
	OfferDocs
	put     []string
	deleted []string
}

func (f *fakeDocs) Put(_ context.Context, key string, _ io.Reader, _ int64, _ string) error {
	f.put = append(f.put, key)
	return nil
}
func (f *fakeDocs) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func TestUploadAttachment(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), 200)...)
	s, _, _ := mailSvc(t)
	at, docs := &fakeAttach{}, &fakeDocs{}
	s.Attach, s.Docs, s.Scanner = at, docs, fakeScan{clean: true}
	ctx := context.Background()

	a, err := s.UploadAttachment(ctx, decider(), job, app, "Contract.PDF", pdf)
	if err != nil || a.ContentType != "application/pdf" || a.Size != int64(len(pdf)) || len(at.added) != 1 || len(docs.put) != 1 {
		t.Fatalf("upload = %+v, %v", a, err)
	}
	if !strings.HasPrefix(docs.put[0], "m/") || strings.Contains(a.ObjectKey, "Contract") {
		t.Fatalf("object key = %q", docs.put[0])
	}
	for name, c := range map[string]struct {
		file string
		data []byte
	}{
		"program named pdf": {"a.pdf", []byte("MZ\x90\x00\x03 not a pdf at all")},
		"bad extension":     {"a.exe", pdf},
		"empty":             {"a.pdf", nil},
		"too big":           {"a.pdf", append([]byte("%PDF-"), make([]byte, MaxAttachmentBytes)...)},
		"control char name": {"a\x00.pdf", pdf},
	} {
		if _, err := s.UploadAttachment(ctx, decider(), job, app, c.file, c.data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// a path in the name is cut to the base name
	if a, err = s.UploadAttachment(ctx, decider(), job, app, `C:\docs\..\offer.pdf`, pdf); err != nil || a.Filename != "offer.pdf" {
		t.Fatalf("path name = %+v, %v", a, err)
	}
	// infected, and a scanner that cannot answer, both refuse (fail closed) and store nothing
	n := len(docs.put)
	s.Scanner = fakeScan{clean: false}
	if _, err = s.UploadAttachment(ctx, decider(), job, app, "a.pdf", pdf); !errors.Is(err, ErrInfected) {
		t.Fatalf("infected = %v", err)
	}
	s.Scanner = fakeScan{err: errors.New("down")}
	if _, err = s.UploadAttachment(ctx, decider(), job, app, "a.pdf", pdf); err == nil {
		t.Fatal("unscanned file accepted")
	}
	if len(docs.put) != n {
		t.Fatal("a refused file was stored")
	}
	s.Scanner = nil
	if _, err = s.UploadAttachment(ctx, decider(), job, app, "a.pdf", pdf); !errors.Is(err, ErrNoAttachments) {
		t.Fatalf("no scanner = %v", err)
	}
}

func TestCleanAttachmentIDs(t *testing.T) {
	a, b := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	if got, err := cleanAttachmentIDs([]string{a, a, b}); err != nil || len(got) != 2 {
		t.Fatalf("dedupe = %v, %v", got, err)
	}
	if _, err := cleanAttachmentIDs([]string{a, b, a[:35] + "3", "33333333-3333-4333-8333-333333333333"}); err == nil {
		t.Fatal("four files accepted")
	}
	if _, err := cleanAttachmentIDs([]string{"nope"}); err == nil {
		t.Fatal("bad id accepted")
	}
}
