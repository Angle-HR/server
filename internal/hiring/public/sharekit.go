package public

import (
	"net/url"
	"strings"

	"github.com/Angle-HR/server/pkg/formschema"
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

// ValidSource reports whether src is a share channel the kit issues (or "direct"). It lives in formschema so the
// application side does not import this package.
func ValidSource(src string) bool { return formschema.ValidSource(src) }

// BuildShareKit builds the share links for a published job. siteURL has no trailing slash.
func BuildShareKit(siteURL, publicID, title, company string) ShareKit {
	base := strings.TrimRight(siteURL, "/") + "/j/" + publicID
	text := title
	if company != "" {
		text = title + " at " + company
	}
	kit := ShareKit{JobURL: base}
	for _, c := range formschema.ShareChannels {
		link := base + "?src=" + c.Key
		var u string
		switch c.Key {
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
		kit.Links = append(kit.Links, ShareLink{Channel: c.Key, Label: c.Label, URL: u})
	}
	return kit
}
