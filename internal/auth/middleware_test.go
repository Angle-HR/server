package auth

import "testing"

func TestBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "bearer prefix", header: "Bearer abc.def.ghi", want: "abc.def.ghi"},
		{name: "bearer lowercase", header: "bearer abc.def.ghi", want: "abc.def.ghi"},
		{name: "bare token", header: "abc.def.ghi", want: "abc.def.ghi"},
		{name: "trimmed bare token", header: "  abc.def.ghi  ", want: "abc.def.ghi"},
		{name: "empty", header: "", want: ""},
		{name: "bearer only", header: "Bearer", want: ""},
		{name: "bearer empty token", header: "Bearer ", want: ""},
		{name: "other scheme", header: "Basic abc", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bearerToken(tc.header); got != tc.want {
				t.Fatalf("bearerToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
