package uploads

import (
	"context"
	"fmt"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/pkg/formschema"
)

// VerifyInput names an upload a candidate has PUT, before the application is submitted.
type VerifyInput struct {
	PublicID   string
	QuestionID string
	Token      string
}

// Verify runs the check Submit runs for an upload answer (object present, size, type, content, malware scan) so a
// candidate can learn about a bad file early. It is Claim without an application: it attaches nothing, and a
// repeat (or the later Submit) does not scan again once the file is clean. A file that fails is rejected for good,
// exactly as it would be on submit.
func (s *Service) Verify(ctx context.Context, in VerifyInput) (apply.File, error) {
	entry, err := s.Jobs.LookupJob(ctx, in.PublicID)
	if err != nil {
		return apply.File{}, err
	}
	rj, err := s.Jobs.Regional(entry.Region)
	if err != nil {
		return apply.File{}, fmt.Errorf("uploads: region %q: %w", entry.Region, err)
	}
	t, err := rj.LoadForApply(ctx, entry.OrganizationID, in.PublicID)
	if err != nil {
		return apply.File{}, err
	}
	if t.Status == formschema.StatusArchived {
		return apply.File{}, apply.ErrGone
	}
	var q *formschema.FormQuestion
	for i := range t.Questions {
		if t.Questions[i].ID == in.QuestionID {
			q = &t.Questions[i]
		}
	}
	if q == nil || !apply.IsUpload(q.Type) {
		return apply.File{}, fieldErr("question_id", "this question does not take a file")
	}
	return s.Claim(ctx, apply.ClaimRequest{
		Region: entry.Region, TenantID: entry.OrganizationID, JobID: t.JobID, QuestionID: q.ID,
		Token: in.Token, Question: q,
	})
}
