package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Angle-HR/server/pkg/formschema"
)

// Compare limits.
const (
	MinCompare = 2
	MaxCompare = 4
)

// CompareItem is one candidate of a comparison. It holds what a reviewer weighs candidates on: the answers are in
// CompareResult.Rows. It carries no review flags and no status note, and the answers to special category questions
// are never here (they live in the restricted store and are not read).
type CompareItem struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Email       string      `json:"email"`
	Country     string      `json:"country"`
	Status      string      `json:"status"`
	StageID     string      `json:"stage_id,omitempty"`
	SubmittedAt time.Time   `json:"submitted_at"`
	Tags        []Tag       `json:"tags"`
	Files       []File      `json:"files"`
	Interviews  []Interview `json:"interviews"`
}

// CompareRow is one question with each candidate's answer, in the order of CompareResult.Applications.
type CompareRow struct {
	QuestionID string `json:"question_id"`
	Section    string `json:"section"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	// Values has one entry per candidate: the answer, or null when they gave none.
	Values []json.RawMessage `json:"values"`
	// FileIDs has one entry per candidate for upload questions: the file's id, or "".
	FileIDs []string `json:"file_ids"`
}

// CompareResult is a side-by-side view of 2 to 4 applications of one job.
type CompareResult struct {
	Applications []CompareItem `json:"applications"`
	Rows         []CompareRow  `json:"rows"`
}

// Compare lays applications of a job side by side. Each is opened, which is audited like any opening, but a
// comparison never makes the caller the reviewer of a flagged application: that needs opening it alone.
func (s *Service) Compare(ctx context.Context, c Caller, jobID string, ids []string) (*CompareResult, error) {
	if _, err := s.access(ctx, c, jobID); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var uniq []string
	for _, id := range ids {
		if !formschema.IsUUID(id) {
			return nil, ErrNotFound
		}
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) < MinCompare || len(uniq) > MaxCompare {
		return nil, invalid("ids", fmt.Sprintf("choose between %d and %d applications", MinCompare, MaxCompare))
	}
	out := &CompareResult{Applications: []CompareItem{}, Rows: []CompareRow{}}
	rowAt := map[string]int{}
	for i, id := range uniq {
		d, err := s.Store.Open(ctx, c.OrgID, OpenInput{JobID: jobID, AppID: id, ActorID: c.UserID})
		if err != nil {
			return nil, err
		}
		item := CompareItem{ID: d.ID, Name: d.Name, Email: d.Email, Country: d.Country, Status: d.Status, StageID: d.StageID,
			SubmittedAt: d.SubmittedAt, Tags: d.Tags, Files: d.Files, Interviews: []Interview{}}
		if item.Tags == nil {
			item.Tags = []Tag{}
		}
		if item.Files == nil {
			item.Files = []File{}
		}
		if s.Schedule != nil {
			if list, lerr := s.Schedule.Interviews(ctx, c.OrgID, jobID, id); lerr == nil && list != nil {
				item.Interviews = list
			}
		}
		out.Applications = append(out.Applications, item)
		for _, a := range d.Answers {
			at, ok := rowAt[a.QuestionID]
			if !ok {
				at = len(out.Rows)
				rowAt[a.QuestionID] = at
				out.Rows = append(out.Rows, CompareRow{QuestionID: a.QuestionID, Section: a.Section, Type: a.Type, Label: a.Label,
					Values: make([]json.RawMessage, len(uniq)), FileIDs: make([]string, len(uniq))})
			}
			if len(a.Value) > 0 {
				out.Rows[at].Values[i] = a.Value
			}
			out.Rows[at].FileIDs[i] = a.FileID
		}
	}
	return out, nil
}
