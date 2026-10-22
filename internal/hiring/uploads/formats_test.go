package uploads

import (
	"strings"
	"testing"
)

func TestCleanFilename(t *testing.T) {
	cases := map[string]string{
		"cv.pdf":                          "cv.pdf",
		"../../etc/passwd":                "passwd",
		`C:\Users\me\My CV.pdf`:           "My CV.pdf",
		"a\x00b\nc.pdf":                   "abc.pdf",
		"  spaced.pdf  ":                  "spaced.pdf",
		"..":                              "..",
		"":                                "",
		"/":                               "",
		strings.Repeat("a", 300) + ".pdf": strings.Repeat("a", 116) + ".pdf",
	}
	for in, want := range cases {
		if got := cleanFilename(in); got != want {
			t.Errorf("cleanFilename(%.20q) = %.30q, want %.30q", in, got, want)
		}
	}
}

func TestMatchNeedsTypeAndExtensionToAgree(t *testing.T) {
	if _, ok := match([]string{"pdf"}, "application/pdf; charset=binary", "CV.PDF"); !ok {
		t.Error("upper-case extension and a parameter on the type must be tolerated")
	}
	for _, c := range []struct{ ct, name string }{
		{"application/pdf", "cv.exe"}, {"application/x-msdownload", "cv.pdf"}, {"application/pdf", "cv"}, {"", "cv.pdf"},
	} {
		if _, ok := match([]string{"pdf"}, c.ct, c.name); ok {
			t.Errorf("%q %q must not match", c.ct, c.name)
		}
	}
}

func TestMagicBytes(t *testing.T) {
	good := map[string][]byte{
		"pdf": []byte("%PDF-1.4"), "png": []byte("\x89PNG\r\n\x1a\n\x00"), "jpeg": {0xFF, 0xD8, 0xFF, 0xE0},
		"webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), "docx": []byte("PK\x03\x04rest"), "odt": []byte("PK\x03\x04rest"),
	}
	for name, b := range good {
		if !formats[name].magic(b) {
			t.Errorf("%s: valid header rejected", name)
		}
		if formats[name].magic([]byte("<html>")) || formats[name].magic(nil) {
			t.Errorf("%s: accepted a non-file", name)
		}
	}
	if formats["webp"].magic([]byte("RIFF\x10\x00\x00\x00WAVEfmt ")) {
		t.Error("a WAV is not a WebP")
	}
}
