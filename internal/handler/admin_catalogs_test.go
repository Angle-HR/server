package handler

import (
	"errors"
	"testing"

	"github.com/Angle-HR/server/pkg/apperror"
)

func strPtr(s string) *string { return &s }

func columnNames(cols []catalogColumn) []string {
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	return names
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestInsertCatalogColumns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		def  catalogDef
		body catalogCreateBody
		want []string
		fail bool
	}{
		{"label", catalogDef{hasLabel: true}, catalogCreateBody{Label: strPtr(" Small ")},
			[]string{"label", "min_size", "max_size", "sort_order"}, false},
		{"label required", catalogDef{hasLabel: true}, catalogCreateBody{}, nil, true},
		{"description", catalogDef{hasDesc: true}, catalogCreateBody{Description: strPtr("d"), Slug: strPtr("s")},
			[]string{"description", "slug", "emoji", "sort_order", "is_active"}, false},
		{"region ok", catalogDef{hasRegion: true},
			catalogCreateBody{Name: strPtr("n"), Slug: strPtr("s"), Region: strPtr("uk")},
			[]string{"name", "slug", "region", "icon_key", "sort_order", "is_active"}, false},
		{"region invalid", catalogDef{hasRegion: true},
			catalogCreateBody{Name: strPtr("n"), Slug: strPtr("s"), Region: strPtr("mars")}, nil, true},
		{"icon url", catalogDef{hasIconURL: true},
			catalogCreateBody{Name: strPtr("n"), Slug: strPtr("s"), IconURL: strPtr("u")},
			[]string{"name", "slug", "icon_url", "sort_order", "is_active"}, false},
		{"emoji", catalogDef{hasEmoji: true}, catalogCreateBody{Name: strPtr("n"), Slug: strPtr("s")},
			[]string{"name", "slug", "emoji", "sort_order", "is_active"}, false},
		{"icon key", catalogDef{hasIconKey: true}, catalogCreateBody{Name: strPtr("n"), Slug: strPtr("s")},
			[]string{"name", "slug", "icon_key", "sort_order", "is_active"}, false},
		{"plain", catalogDef{}, catalogCreateBody{Name: strPtr("n"), Slug: strPtr("s")},
			[]string{"name", "slug", "sort_order", "is_active"}, false},
		{"plain requires name and slug", catalogDef{}, catalogCreateBody{Name: strPtr("n")}, nil, true},
	}
	for _, c := range cases {
		cols, err := insertCatalogColumns(c.def, &c.body)
		if c.fail {
			var appErr *apperror.AppError
			if !errors.As(err, &appErr) {
				t.Errorf("%s: expected validation error, got %v", c.name, err)
			}
			continue
		}
		if err != nil || !equalStrings(columnNames(cols), c.want) {
			t.Errorf("%s: got %v err %v, want %v", c.name, columnNames(cols), err, c.want)
		}
	}
}

func TestUpdateCatalogColumns(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		def  catalogDef
		want []string
	}{
		{"label", catalogDef{hasLabel: true}, []string{"label", "min_size", "max_size", "sort_order"}},
		{"desc", catalogDef{hasDesc: true}, []string{"description", "slug", "emoji", "sort_order", "is_active"}},
		{"region", catalogDef{hasRegion: true}, []string{"name", "slug", "region", "icon_key", "sort_order", "is_active"}},
		{"icon url", catalogDef{hasIconURL: true}, []string{"name", "slug", "icon_url", "sort_order", "is_active"}},
		{"emoji", catalogDef{hasEmoji: true}, []string{"name", "slug", "emoji", "sort_order", "is_active"}},
		{"icon key", catalogDef{hasIconKey: true}, []string{"name", "slug", "icon_key", "sort_order", "is_active"}},
		{"plain", catalogDef{}, []string{"name", "slug", "sort_order", "is_active"}},
	}
	for _, c := range cases {
		cols, err := updateCatalogColumns(c.def, &catalogCreateBody{})
		if err != nil || !equalStrings(columnNames(cols), c.want) {
			t.Errorf("%s: got %v err %v, want %v", c.name, columnNames(cols), err, c.want)
		}
	}
	if _, err := updateCatalogColumns(catalogDef{hasRegion: true}, &catalogCreateBody{Region: strPtr("mars")}); err == nil {
		t.Error("expected invalid region error")
	}
}
