package mailer

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Angle-HR/server/pkg/config"
)

// AppLinkURLs resolves the two origins every email link is built from. APP_URL (product: invite, password reset,
// dashboard links) and ADMIN_APP_URL (admin invite link) are the only sources; nothing else in the code base
// hardcodes a frontend origin. Both must be absolute http(s) URLs without a query or fragment when set. In
// production-like environments (production, prod, staging) both are required, and must be https.
// Outside them an unset value is accepted: the mailer then refuses the email types that need it.
func AppLinkURLs(appEnv, appURL, adminAppURL string) (app, admin string, err error) {
	hardened := config.HardenedEnv(appEnv)
	check := func(name, raw string) (string, error) {
		raw = strings.TrimRight(strings.TrimSpace(raw), "/")
		if raw == "" {
			if hardened {
				return "", fmt.Errorf("%s is required when APP_ENV=%s (it is the origin of the links in emails)", name, appEnv)
			}
			return "", nil
		}
		u, perr := url.Parse(raw)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("%s must be an absolute http(s) URL, got %q", name, raw)
		}
		if hardened && u.Scheme != "https" {
			return "", fmt.Errorf("%s must use https when APP_ENV=%s", name, appEnv)
		}
		return raw, nil
	}
	if app, err = check("APP_URL", appURL); err != nil {
		return "", "", err
	}
	if admin, err = check("ADMIN_APP_URL", adminAppURL); err != nil {
		return "", "", err
	}
	return app, admin, nil
}

// AppLinkURLsFromEnv is AppLinkURLs over APP_ENV, APP_URL and ADMIN_APP_URL.
func AppLinkURLsFromEnv() (app, admin string, err error) {
	return AppLinkURLs(os.Getenv("APP_ENV"), os.Getenv("APP_URL"), os.Getenv("ADMIN_APP_URL"))
}
