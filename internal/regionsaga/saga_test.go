package regionsaga

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/region"
)

// memStore is an in-memory Store with the same semantics as PGStore.
type memStore struct {
	mu    sync.Mutex
	sagas map[uuid.UUID]*Saga
	lease map[uuid.UUID]bool
}

func newMemStore() *memStore {
	return &memStore{sagas: map[uuid.UUID]*Saga{}, lease: map[uuid.UUID]bool{}}
}

func (m *memStore) Begin(_ context.Context, user uuid.UUID, target region.Region) (Saga, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var done *Saga
	for _, s := range m.sagas {
		if s.UserID != user {
			continue
		}
		switch {
		case !s.Terminal():
			if s.Target != target {
				return Saga{}, ErrConflict
			}
			return *s, nil
		case s.State == StateCompleted:
			done = s
		}
	}
	if done != nil {
		if done.Target != target {
			return Saga{}, ErrConflict
		}
		return *done, nil
	}
	s := &Saga{ID: uuid.New(), UserID: user, Target: target, State: StateStarted, UpdatedAt: time.Now()}
	m.sagas[s.ID] = s
	return *s, nil
}

func (m *memStore) Claim(_ context.Context, id uuid.UUID, _ time.Duration) (Saga, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sagas[id]
	if s == nil || s.Terminal() || m.lease[id] {
		return Saga{}, false, nil
	}
	m.lease[id] = true
	s.Attempts++
	return *s, true, nil
}

func (m *memStore) Advance(_ context.Context, id uuid.UUID, from, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sagas[id]
	if s == nil || s.State != from {
		return errors.New("not in state")
	}
	s.State, s.UpdatedAt = to, time.Now()
	return nil
}

func (m *memStore) Fail(_ context.Context, id uuid.UUID, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sagas[id].LastError = msg
	return nil
}

func (m *memStore) Release(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.lease, id)
	return nil
}

func (m *memStore) Get(_ context.Context, id uuid.UUID) (Saga, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.sagas[id], nil
}

func (m *memStore) Resumable(_ context.Context, cutoff time.Time, limit int) ([]Saga, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Saga
	for _, s := range m.sagas {
		if !s.Terminal() && !m.lease[s.ID] && s.UpdatedAt.Before(cutoff) {
			out = append(out, *s)
		}
	}
	return out, nil
}

// world is a fake identity + org with failure injection and call recording.
type world struct {
	mu       sync.Mutex
	calls    []string
	failOnce map[string]error // consumed on first use
	failAll  map[string]error
	userRow  bool // copied into the target region
	orgRow   bool
	pending  bool // identity pending user
	registry string
	orgPend  bool
	events   map[string]int
}

func newWorld() *world {
	return &world{failOnce: map[string]error{}, failAll: map[string]error{}, pending: true, registry: "global", orgPend: true, events: map[string]int{}}
}

func (w *world) hit(op string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, op)
	if err, ok := w.failOnce[op]; ok {
		delete(w.failOnce, op)
		return err
	}
	return w.failAll[op]
}

func (w *world) count(op string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		if c == op {
			n++
		}
	}
	return n
}

func (w *world) Prepare(context.Context, uuid.UUID, region.Region) error {
	if err := w.hit("prepare"); err != nil {
		return err
	}
	w.userRow = true
	return nil
}

func (w *world) Commit(context.Context, uuid.UUID, region.Region) error {
	if err := w.hit("commit"); err != nil {
		return err
	}
	w.registry, w.pending = "eu", false
	return nil
}

func (w *world) Abort(context.Context, uuid.UUID, region.Region) error {
	if err := w.hit("abort"); err != nil {
		return err
	}
	if w.registry != "global" {
		return Permanent{ErrCommitted}
	}
	w.userRow = false
	return nil
}

func (w *world) CopyPending(context.Context, uuid.UUID, region.Region) (string, error) {
	if err := w.hit("copy"); err != nil {
		return "", err
	}
	w.orgRow = true
	return "company-1", nil
}

func (w *world) UndoCopy(context.Context, uuid.UUID, region.Region) error {
	if err := w.hit("undo"); err != nil {
		return err
	}
	w.orgRow = false
	return nil
}

func (w *world) ReleasePending(context.Context, uuid.UUID) error {
	if err := w.hit("release"); err != nil {
		return err
	}
	w.orgPend = false
	return nil
}

func (w *world) Finalize(_ context.Context, s Saga) error {
	if err := w.hit("finalize"); err != nil {
		return err
	}
	w.events[s.UserID.String()+"/"+string(s.Target)]++
	return nil
}

func coordinator(st Store, w *world) *Coordinator {
	return &Coordinator{Store: st, Identity: w, Org: w, StepBackoff: time.Millisecond, Wait: 200 * time.Millisecond}
}

var errBoom = errors.New("boom")

func TestHappyPathRunsStepsInOrder(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	reg, err := coordinator(st, w).Move(context.Background(), u, region.RegionEU)
	if err != nil || reg != region.RegionEU {
		t.Fatalf("%v %v", reg, err)
	}
	want := []string{"prepare", "copy", "commit", "release", "finalize"}
	if len(w.calls) != len(want) {
		t.Fatalf("calls %v", w.calls)
	}
	for i := range want {
		if w.calls[i] != want[i] {
			t.Fatalf("calls %v", w.calls)
		}
	}
	if !w.userRow || !w.orgRow || w.pending || w.orgPend || w.registry != "eu" {
		t.Fatalf("%+v", w)
	}
}

func TestMoveIsIdempotentAfterCompletion(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	c := coordinator(st, w)
	for range 3 {
		if _, err := c.Move(context.Background(), u, region.RegionEU); err != nil {
			t.Fatal(err)
		}
	}
	if w.count("prepare") != 1 || w.events[u.String()+"/eu"] != 1 || len(st.sagas) != 1 {
		t.Fatalf("calls %v events %v", w.calls, w.events)
	}
	if _, err := c.Move(context.Background(), u, region.RegionUS); !errors.Is(err, ErrConflict) {
		t.Fatalf("moving a moved account elsewhere: %v", err)
	}
}

func TestTransientStepFailureIsRetriedInPlace(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	w.failOnce["copy"] = errBoom
	if _, err := coordinator(st, w).Move(context.Background(), u, region.RegionEU); err != nil {
		t.Fatal(err)
	}
	if w.count("copy") != 2 || w.count("undo") != 0 {
		t.Fatalf("calls %v", w.calls)
	}
}

func TestFailureBeforeCommitCompensates(t *testing.T) {
	for _, step := range []string{"prepare", "copy", "commit"} {
		t.Run(step, func(t *testing.T) {
			st, w, u := newMemStore(), newWorld(), uuid.New()
			w.failAll[step] = Permanent{errBoom}
			_, err := coordinator(st, w).Move(context.Background(), u, region.RegionEU)
			if !errors.Is(err, ErrCompensated) {
				t.Fatalf("want ErrCompensated, got %v", err)
			}
			// The account is exactly as before: the holding rows are intact and nothing is left in the region.
			if w.userRow || w.orgRow || !w.pending || !w.orgPend || w.registry != "global" {
				t.Fatalf("not rolled back: %+v", w)
			}
			if w.count("undo") != 1 || w.count("abort") != 1 || w.count("release") != 0 || len(w.events) != 0 {
				t.Fatalf("calls %v", w.calls)
			}
			var s Saga
			for _, v := range st.sagas {
				s = *v
			}
			if s.State != StateCompensated || s.LastError == "" {
				t.Fatalf("%+v", s)
			}
			// After the fault is gone the same user can move.
			delete(w.failAll, step)
			if reg, err := coordinator(st, w).Move(context.Background(), u, region.RegionEU); err != nil || reg != region.RegionEU {
				t.Fatalf("retry after compensation: %v %v", reg, err)
			}
			if w.registry != "eu" || len(st.sagas) != 2 {
				t.Fatalf("%+v sagas=%d", w, len(st.sagas))
			}
		})
	}
}

func TestStalledCompensationResumes(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	w.failAll["copy"] = Permanent{errBoom}
	w.failAll["abort"] = errBoom
	c := coordinator(st, w)
	if _, err := c.Move(context.Background(), u, region.RegionEU); err == nil || errors.Is(err, ErrCompensated) {
		t.Fatalf("a stuck compensation is an error, not a clean rollback: %v", err)
	}
	var s Saga
	for _, v := range st.sagas {
		s = *v
	}
	if s.State != StateCompensating {
		t.Fatalf("state %s", s.State)
	}
	delete(w.failAll, "abort")
	if n, err := c.ResumeOnce(context.Background(), -time.Second, 10); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	for _, v := range st.sagas {
		s = *v
	}
	if s.State != StateCompensated || w.userRow || !w.pending {
		t.Fatalf("%+v %+v", s, w)
	}
}

func TestFailureAfterCommitRollsForwardOnly(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	w.failAll["release"] = errBoom
	c := coordinator(st, w)
	if _, err := c.Move(context.Background(), u, region.RegionEU); err == nil || errors.Is(err, ErrCompensated) {
		t.Fatalf("want a plain error, got %v", err)
	}
	if w.count("abort") != 0 || w.count("undo") != 0 || w.registry != "eu" || !w.userRow || !w.orgRow {
		t.Fatalf("a committed move must not be undone: %v %+v", w.calls, w)
	}
	// A repeated request continues the same saga once the fault is gone.
	delete(w.failAll, "release")
	reg, err := c.Move(context.Background(), u, region.RegionEU)
	if err != nil || reg != region.RegionEU || len(st.sagas) != 1 {
		t.Fatalf("%v %v sagas=%d", reg, err, len(st.sagas))
	}
	if w.count("prepare") != 1 || w.count("commit") != 1 || w.orgPend || w.events[u.String()+"/eu"] != 1 {
		t.Fatalf("calls %v events %v", w.calls, w.events)
	}
}

func TestResumerFinishesACrashedSaga(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	// A process died after the commit: state identity_committed, pending rows still there, no lease held.
	s, _ := st.Begin(context.Background(), u, region.RegionEU)
	w.userRow, w.orgRow, w.registry, w.pending = true, true, "eu", false
	st.sagas[s.ID].State = StateIdentityCommitted
	st.sagas[s.ID].UpdatedAt = time.Now().Add(-time.Hour)

	c := coordinator(st, w)
	if n, err := c.ResumeOnce(context.Background(), time.Minute, 10); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if st.sagas[s.ID].State != StateCompleted || w.orgPend || w.events[u.String()+"/eu"] != 1 {
		t.Fatalf("%+v %+v", st.sagas[s.ID], w)
	}
	if n, _ := c.ResumeOnce(context.Background(), time.Minute, 10); n != 0 {
		t.Fatalf("a finished saga is not resumed again: %d", n)
	}
}

func TestConflictingTargetAndInvalidTarget(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	c := coordinator(st, w)
	if _, err := st.Begin(context.Background(), u, region.RegionEU); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Move(context.Background(), u, region.RegionUS); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
	for _, bad := range []region.Region{region.RegionGlobal, "mars", ""} {
		if _, err := c.Move(context.Background(), u, bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestConcurrentMovesRunTheStepsOnce(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	c := coordinator(st, w)
	c.Wait = 2 * time.Second
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Move(context.Background(), u, region.RegionEU)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if w.count("prepare") != 1 || w.count("commit") != 1 || w.events[u.String()+"/eu"] != 1 {
		t.Fatalf("calls %v", w.calls)
	}
}

func TestRequestAfterStalledCompensationStartsAFreshAttempt(t *testing.T) {
	st, w, u := newMemStore(), newWorld(), uuid.New()
	w.failAll["prepare"] = errBoom // identity is down
	w.failAll["abort"] = errBoom
	c := coordinator(st, w)
	if _, err := c.Move(context.Background(), u, region.RegionEU); err == nil {
		t.Fatal("the first request fails")
	}
	delete(w.failAll, "prepare")
	delete(w.failAll, "abort") // identity is back
	reg, err := c.Move(context.Background(), u, region.RegionEU)
	if err != nil || reg != region.RegionEU {
		t.Fatalf("the retry must unwind the old attempt and succeed: %v %v", reg, err)
	}
	if w.registry != "eu" || len(st.sagas) != 2 {
		t.Fatalf("%+v sagas=%d", w, len(st.sagas))
	}
}
