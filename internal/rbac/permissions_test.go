package rbac

import "testing"

func TestSet(t *testing.T) {
	s := NewSet(JobCreate, JobDeleteHard, FormAskPayHistory, FormEDIViewNamed)
	if !s.Has(JobCreate) {
		t.Error("JobCreate should be granted")
	}
	for _, p := range []Permission{JobDeleteHard, FormAskPayHistory, FormEDIViewNamed} {
		if s.Has(p) {
			t.Errorf("%s must never be granted", p)
		}
	}
	if s.Has(JobPublishExternal) {
		t.Error("ungranted permission reported as granted")
	}
}
