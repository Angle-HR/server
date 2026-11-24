package mailer

import "testing"

func TestAppLinkURLs(t *testing.T) {
	tests := []struct {
		name, env, app, admin string
		wantApp, wantAdmin    string
		wantErr               bool
	}{
		{"dev unset ok", "development", "", "", "", "", false},
		{"dev trims slash", "", "http://localhost:3000/", "http://localhost:3001", "http://localhost:3000", "http://localhost:3001", false},
		{"dev bad scheme", "development", "ftp://x", "", "", "", true},
		{"dev relative", "development", "localhost:3000", "", "", "", true},
		{"dev query", "development", "http://a.test/?x=1", "", "", "", true},
		{"staging missing app", "staging", "", "https://admin.test", "", "", true},
		{"staging missing admin", "staging", "https://app.test", "", "", "", true},
		{"prod http refused", "production", "http://app.test", "https://admin.test", "", "", true},
		{"prod ok", "production", "https://app.test/", "https://admin.test", "https://app.test", "https://admin.test", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, admin, err := AppLinkURLs(tc.env, tc.app, tc.admin)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if app != tc.wantApp || admin != tc.wantAdmin {
				t.Fatalf("got %q %q", app, admin)
			}
		})
	}
}

func TestEmailLinksUseConfiguredOrigins(t *testing.T) {
	m := &Mailer{cfg: Config{AppURL: "https://app.example", AdminAppURL: "https://admin.example"}}
	for typ, want := range map[string]string{
		TypePasswordReset: "https://app.example",
		TypeOrgInvite:     "https://app.example",
		TypeAdminInvite:   "https://admin.example",
	} {
		_, _, base, err := m.selectTemplate(typ)
		if err != nil || base != want {
			t.Fatalf("%s: base %q err %v, want %q", typ, base, err, want)
		}
	}
	empty := &Mailer{}
	if _, _, _, err := empty.selectTemplate(TypePasswordReset); err == nil {
		t.Fatal("password reset without APP_URL must fail")
	}
}
