package kyb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// registryGet fetches baseURL+path and decodes a JSON body into out. notFound is true for a 404. A non-nil
// error means the check could not run (see ErrRegistryUnavailable, ErrRateLimited, ErrRegistryAuth).
// A 400 is reported as notFound: the keyless registries answer 400 for a malformed number.
func registryGet(ctx context.Context, client *http.Client, baseURL, path string, out any) (notFound bool, err error) {
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+path, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrRegistryUnavailable, err)
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		// Do not wrap err: its text can contain the request URL.
		return false, ErrRegistryUnavailable
	}
	defer closeBody(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusBadRequest:
		return true, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, ErrRegistryAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		return false, ErrRateLimited
	case resp.StatusCode >= http.StatusInternalServerError:
		return false, ErrRegistryUnavailable
	default:
		return false, fmt.Errorf("%w: unexpected status %d", ErrRegistryUnavailable, resp.StatusCode)
	}
	if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); decodeErr != nil {
		return false, fmt.Errorf("%w: invalid response", ErrRegistryUnavailable)
	}
	return false, nil
}

// assessRecord compares a registry record with the submission. Order matters: an inactive entity is a hard
// block whatever else differs.
func assessRecord(req *Request, name string, registry *Address, inactive bool) Result {
	res := Result{RegistryName: name, RegisteredAddress: FormatAddress(registry)}
	switch {
	case inactive:
		res.Outcome, res.Reason = OutcomeFailed, ReasonInactiveEntity
	case !NamesMatch(req.LegalName, name):
		res.Outcome, res.Reason = OutcomeFailed, ReasonNameMismatch
	case !AddressesMatch(&req.Address, registry):
		res.Outcome, res.Reason = OutcomeFailed, ReasonAddressMismatch
	default:
		res.Outcome = OutcomeVerified
	}
	return res
}
