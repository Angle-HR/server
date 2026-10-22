package retention_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/retention"
	"github.com/Angle-HR/server/internal/hiring/uploads"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	due       map[string][]retention.Due
	deleted   []string
	failApp   string
	shortened int
	notices   map[string][]retention.NoticeItem
	blocks    int
	log       []string
}

func (f *fakeStore) ShortenForClosedJobs(context.Context, string, time.Time) (int, error) {
	f.log = append(f.log, "shorten")
	return f.shortened, nil
}
func (f *fakeStore) DueApplications(_ context.Context, tid string, _ time.Time, limit int) ([]retention.Due, error) {
	list := f.due[tid]
	if len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}
func (f *fakeStore) DeleteApplication(_ context.Context, tid string, d retention.Due, _ time.Time) error {
	if d.ID == f.failApp {
		return errors.New("db down")
	}
	f.log = append(f.log, "delete:"+d.ID)
	f.deleted = append(f.deleted, d.ID)
	rest := f.due[tid][:0:0]
	for _, x := range f.due[tid] {
		if x.ID != d.ID {
			rest = append(rest, x)
		}
	}
	f.due[tid] = rest
	return nil
}
func (f *fakeStore) DeleteExpiredBlocks(context.Context, string, time.Time) (int, error) {
	return f.blocks, nil
}
func (f *fakeStore) DeleteOldEmails(context.Context, string, time.Time) (int, error) { return 0, nil }
func (f *fakeStore) QueueNotice(_ context.Context, tid string, _ time.Time, items []retention.NoticeItem) error {
	if f.notices == nil {
		f.notices = map[string][]retention.NoticeItem{}
	}
	f.notices[tid] = items
	return nil
}

type fakeStorage struct {
	objects map[string]bool
	removed []string
	failKey string
}

func (s *fakeStorage) Remove(_ context.Context, key string) error {
	if key == s.failKey {
		return errors.New("r2 down")
	}
	delete(s.objects, key)
	s.removed = append(s.removed, key)
	return nil
}

func sweeper(st *fakeStore, sg *fakeStorage, tenants ...string) *retention.Sweeper {
	return &retention.Sweeper{
		Tenants: func(context.Context) ([]string, error) { return tenants, nil },
		Store:   st, Storage: sg, Now: func() time.Time { return now },
	}
}

func TestDeletesFilesThenApplicationsAndNotifiesTheCompany(t *testing.T) {
	st := &fakeStore{shortened: 2, blocks: 1, due: map[string][]retention.Due{
		"org-1": {
			{ID: "a1", JobID: "j1", JobCode: "JB-1", JobTitle: "Engineer", FileIDs: []string{"f1"}},
			{ID: "a2", JobID: "j1", JobCode: "JB-1", JobTitle: "Engineer"},
			{ID: "a3", JobID: "j2", JobCode: "JB-2", JobTitle: "Designer"},
		},
		"org-2": {},
	}}
	sg := &fakeStorage{objects: map[string]bool{
		uploads.FinalKey("org-1", "j1", "f1"): true, uploads.StagingKey("org-1", "j1", "f1"): true,
	}}
	res, err := sweeper(st, sg, "org-1", "org-2").Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Companies != 2 || res.Deleted != 3 || res.Shortened != 4 || res.Blocks != 2 || res.Emails != 1 || res.Errors != 0 {
		t.Errorf("res = %+v", res)
	}
	if len(sg.objects) != 0 || len(sg.removed) != 2 {
		t.Errorf("both keys of the file must be removed: %v", sg.removed)
	}
	if st.log[0] != "shorten" {
		t.Errorf("retention dates are shortened before anything is deleted: %v", st.log)
	}
	got := st.notices["org-1"]
	if len(got) != 2 || got[0] != (retention.NoticeItem{JobCode: "JB-1", JobTitle: "Engineer", Count: 2}) ||
		got[1] != (retention.NoticeItem{JobCode: "JB-2", JobTitle: "Designer", Count: 1}) {
		t.Errorf("notice = %+v", got)
	}
	if _, sent := st.notices["org-2"]; sent {
		t.Error("a company with nothing deleted gets no email")
	}
}

func TestAFileThatCannotBeRemovedKeepsTheApplication(t *testing.T) {
	st := &fakeStore{due: map[string][]retention.Due{"org-1": {
		{ID: "a1", JobID: "j1", FileIDs: []string{"f1"}},
		{ID: "a2", JobID: "j1"},
	}}}
	sg := &fakeStorage{failKey: uploads.FinalKey("org-1", "j1", "f1")}
	res, err := sweeper(st, sg, "org-1").Run(context.Background())
	if err == nil || res.Errors != 1 || res.Deleted != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(st.deleted) != 1 || st.deleted[0] != "a2" {
		t.Errorf("deleted = %v: the application whose file is still in storage must stay for the next run", st.deleted)
	}
}

func TestAFailingDeleteDoesNotLoopForever(t *testing.T) {
	st := &fakeStore{failApp: "a1", due: map[string][]retention.Due{"org-1": {{ID: "a1", JobID: "j1"}}}}
	done := make(chan struct{})
	go func() {
		_, _ = sweeper(st, &fakeStorage{}, "org-1").Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the sweeper kept retrying a page it could not delete")
	}
}

func TestManyPagesAreAllDeleted(t *testing.T) {
	var list []retention.Due
	for i := 0; i < 250; i++ {
		list = append(list, retention.Due{ID: "a" + string(rune('A'+i%26)) + time.Duration(i).String(), JobID: "j1", JobCode: "JB-1"})
	}
	st := &fakeStore{due: map[string][]retention.Due{"org-1": list}}
	res, err := sweeper(st, &fakeStorage{}, "org-1").Run(context.Background())
	if err != nil || res.Deleted != 250 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if n := st.notices["org-1"][0].Count; n != 250 {
		t.Errorf("notice count = %d", n)
	}
}
