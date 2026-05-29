package query

import "strings"

func normalizeSQL(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}

func mustSQL(sql string, args []any, err error) (string, []any, error) {
	if err != nil {
		return "", nil, err
	}

	return normalizeSQL(sql), args, nil
}
