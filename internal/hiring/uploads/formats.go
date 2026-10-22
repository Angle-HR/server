package uploads

import (
	"bytes"
	"path"
	"strings"
)

// format is one file format a form question can accept.
type format struct {
	name         string
	contentTypes []string
	extensions   []string
	// magic reports whether the first bytes of a file belong to this format.
	magic func(head []byte) bool
}

var formats = map[string]format{
	"pdf": {
		name: "pdf", contentTypes: []string{"application/pdf"}, extensions: []string{".pdf"},
		magic: func(h []byte) bool { return bytes.HasPrefix(h, []byte("%PDF-")) },
	},
	"docx": {
		name:         "docx",
		contentTypes: []string{"application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		extensions:   []string{".docx"}, magic: isZip,
	},
	"odt": {
		name: "odt", contentTypes: []string{"application/vnd.oasis.opendocument.text"},
		extensions: []string{".odt"}, magic: isZip,
	},
	"png": {
		name: "png", contentTypes: []string{"image/png"}, extensions: []string{".png"},
		magic: func(h []byte) bool { return bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")) },
	},
	"jpeg": {
		name: "jpeg", contentTypes: []string{"image/jpeg"}, extensions: []string{".jpg", ".jpeg"},
		magic: func(h []byte) bool { return bytes.HasPrefix(h, []byte{0xFF, 0xD8, 0xFF}) },
	},
	"webp": {
		name: "webp", contentTypes: []string{"image/webp"}, extensions: []string{".webp"},
		magic: func(h []byte) bool {
			return len(h) >= 12 && bytes.HasPrefix(h, []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP"))
		},
	},
}

func isZip(h []byte) bool { return bytes.HasPrefix(h, []byte("PK\x03\x04")) }

// defaultAccept is what a question of each type accepts when its config lists no formats.
func defaultAccept(questionType string) []string {
	switch questionType {
	case "image_upload":
		return []string{"png", "jpeg", "webp"}
	case "file_upload":
		return []string{"pdf", "docx", "odt"}
	default: // autofill_resume, the CV
		return []string{"pdf"}
	}
}

// match finds the accepted format a content type and file name belong to. Both must agree: the content type
// picks the format and the extension must be one of its extensions.
func match(accept []string, contentType, filename string) (format, bool) {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	ext := strings.ToLower(path.Ext(filename))
	for _, name := range accept {
		f, ok := formats[strings.ToLower(name)]
		if !ok {
			continue
		}
		if contains(f.contentTypes, ct) && contains(f.extensions, ext) {
			return f, true
		}
	}
	return format{}, false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// cleanFilename keeps the base name only, drops control characters and limits the length. The name is shown to
// reviewers and never used as a storage key.
func cleanFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "." || out == "/" {
		return ""
	}
	runes := []rune(out)
	if len(runes) > 120 {
		// keep the extension
		ext := path.Ext(out)
		er := []rune(ext)
		if len(er) > 10 {
			er = er[:10]
		}
		out = string(runes[:120-len(er)]) + string(er)
	}
	return out
}
