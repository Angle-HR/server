package bff

import "testing"

func TestPublicPathCareersPages(t *testing.T) {
	for _, p := range []string{"/j/abc123", "/j/abc123/privacy", "/public/jobs/abc123", "/countries", "/", "/api/docs", "/api/openapi.json"} {
		if !publicPath(p, false) {
			t.Errorf("%s must need no session", p)
		}
	}
	for _, p := range []string{"/jobs", "/auth/me", "/organization/agreements", "/api/docs/x"} {
		if publicPath(p, false) {
			t.Errorf("%s must need a session", p)
		}
	}
}
