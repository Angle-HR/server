package appjobs_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The applications side of the monolith must be extractable into its own service. It may share libraries (pkg/*)
// and its own packages, but it may not import anything hiring owns: it learns about jobs through appjobs.JobSource.
// Like TestNoCloudSDKs in pkg/events this parses the imports of every Go file, tests included.

const modulePath = "github.com/Angle-HR/server/"

// applicationsSide lists the directories (relative to internal/) that form the applications side. Every
// subdirectory is included.
var applicationsSide = []string{
	"hiring/apphttp",
	"hiring/appevents",
	"hiring/apply",
	"hiring/applications",
	"hiring/appjobs",
	"hiring/appstore",
	"hiring/reapply",
	"hiring/retention",
	"hiring/uploads",
	"sensitive",
}

// hiringOwned are the packages the applications side must not import.
var hiringOwned = []string{
	"internal/hiring/draft",
	"internal/hiring/drafttest",
	"internal/hiring/jobs",
	"internal/hiring/jobsource",
	"internal/hiring/callerresolver",
	"internal/hiring/jobevents",
	"internal/hiring/public",
	"internal/hiring/hiringtypes",
	"internal/hiring/hiringhttp",
	"internal/hiring/hiringstore",
	"internal/hiring/lifecycle",
	"internal/hiring/gates",
	"internal/hiring/questions",
	"internal/hiring/expiry",
}

func TestApplicationsSideImportsNoHiringPackage(t *testing.T) {
	fset := token.NewFileSet()
	for _, dir := range applicationsSide {
		root := filepath.Join("..", "..", filepath.FromSlash(dir))
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				p = strings.TrimPrefix(p, modulePath)
				for _, banned := range hiringOwned {
					if p == banned || strings.HasPrefix(p, banned+"/") {
						t.Errorf("%s imports %s: the applications side must use appjobs.JobSource and pkg/formschema instead", path, p)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("walk %s: %v", root, err)
		}
	}
}
