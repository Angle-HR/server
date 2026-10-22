package public

import (
	"net/url"
	"strings"
)

// ShareLink is one ready-made link of the Share Kit.
type ShareLink struct {
	Channel string `json:"channel"`
	Label   string `json:"label"`
	URL     string `json:"url"`
}

// ShareKit is what the Share step shows after a job is published.
type ShareKit struct {
	JobURL string      `json:"job_url"`
	Links  []ShareLink `json:"links"`
}

// shareChannels fixes the order and labels. Each channel's job link carries ?src=<channel>, so the application
// can record which link brought the candidate.
var shareChannels = []struct{ key, label string }{
	{"whatsapp", "WhatsApp"}, {"x", "X"}, {"facebook", "Facebook"}, {"linkedin", "LinkedIn"},
	{"telegram", "Telegram"}, {"email", "Email"},
}

// ValidSource reports whether src is a share channel the kit issues (or "direct"), so a stored source is always
// one of a small known set instead of free text from the internet.
func ValidSource(src string) bool {
	if src == "direct" {
		return true
	}
	for _, c := range shareChannels {
		if c.key == src {
			return true
		}
	}
	return false
}

// BuildShareKit builds the share links for a published job. siteURL has no trailing slash.
func BuildShareKit(siteURL, publicID, title, company string) ShareKit {
	base := strings.TrimRight(siteURL, "/") + "/j/" + publicID
	text := title
	if company != "" {
		text = title + " at " + company
	}
	kit := ShareKit{JobURL: base}
	for _, c := range shareChannels {
		link := base + "?src=" + c.key
		var u string
		switch c.key {
		case "whatsapp":
			u = "https://wa.me/?text=" + url.QueryEscape(text+" "+link)
		case "x":
			u = "https://twitter.com/intent/tweet?text=" + url.QueryEscape(text) + "&url=" + url.QueryEscape(link)
		case "facebook":
			u = "https://www.facebook.com/sharer/sharer.php?u=" + url.QueryEscape(link)
		case "linkedin":
			u = "https://www.linkedin.com/sharing/share-offsite/?url=" + url.QueryEscape(link)
		case "telegram":
			u = "https://t.me/share/url?url=" + url.QueryEscape(link) + "&text=" + url.QueryEscape(text)
		case "email":
			u = "mailto:?subject=" + url.PathEscape(text) + "&body=" + url.PathEscape(link)
		}
		kit.Links = append(kit.Links, ShareLink{Channel: c.key, Label: c.label, URL: u})
	}
	return kit
}
