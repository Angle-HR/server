package identityhttp

import (
	"encoding/json"
	"strconv"
)

// Admin list pagination defaults and bounds.
const (
	defaultPageLimit = 50
	maxPageLimit     = 100
	maxPageOffset    = 100000
)

func parseLimit(raw string, def, upper int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	if n > upper {
		return upper
	}
	return n
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
