package draft

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/lifecycle"
	"github.com/Angle-HR/server/internal/hiring/screening"
	"github.com/Angle-HR/server/internal/rbac"
)

// CompanyState is what the publish checks need to know about the company. It lives in the account tables
// (company verification, accepted agreements, settings), so the service reads it through an interface.
type CompanyState = hiringtypes.CompanyState

// Company reads the CompanyState of an organization.
type Company interface {
	State(ctx context.Context, orgID string) (CompanyState, error)
}

// BlockedError means the publish checks failed. It carries the whole report so the page can show every issue.
type BlockedError struct{ Report *gates.Report }

func (e *BlockedError) Error() string { return "draft: publish blocked by checks" }

// ErrInvalidTransition means the job's status does not allow the action.
var ErrInvalidTransition = lifecycle.ErrInvalidTransition

func (s *Service) lc(c Caller) lifecycle.Caller {
	return lifecycle.Caller{UserID: c.UserID, Perms: c.Perms}
}

func mapLifecycleError(err error) error {
	switch {
	case errors.Is(err, lifecycle.ErrForbidden):
		return forbidden(strings.TrimPrefix(err.Error(), "lifecycle: not permitted: "))
	case errors.Is(err, lifecycle.ErrUnknownAction):
		return invalid(fe("action", "unknown action"))
	}
	return err
}

// environment is the data outside the job that the publish checks read.
type environment struct {
	cat      jobs.MarketCatalog
	gateList []gates.Gate
	company  CompanyState
	settings hiringtypes.Settings
}

func (s *Service) loadEnv(ctx context.Context, c Caller) (*environment, error) {
	cat, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	gl, err := s.Ref.Gates(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	env := &environment{cat: cat, gateList: gl, settings: settings}
	if s.Company != nil { // no company reader: every company check fails, which is the safe side
		if env.company, err = s.Company.State(ctx, c.OrgID); err != nil {
			return nil, err
		}
	}
	return env, nil
}

func (s *Service) checkInput(rec *hiringtypes.JobRecord, env *environment) *gates.Input {
	cats := make([]string, len(rec.Declarations))
	for i, d := range rec.Declarations {
		cats[i] = d.Category
	}
	var problems []string
	for _, fe := range screening.Validate(rec.Rules, rec.Form) {
		problems = append(problems, fe.Path+": "+fe.Message)
	}
	return &gates.Input{
		Job: &rec.Job, Form: rec.Form, Catalog: env.cat, Gates: env.gateList, Confirms: rec.Confirmations,
		Categories:      cats,
		CompanyVerified: env.company.Verified, DPAAccepted: env.company.DPAAccepted,
		PrivacyContactSet: env.company.PrivacyContactSet,
		ScreeningEnabled:  env.settings.AutomatedScreeningEnabled, DPIARecorded: env.company.DPIARecorded,
		JobScreeningInScope: rec.DPIAConfirmedAt != "", HasDisqualifyRules: len(rec.Rules) > 0,
		RuleProblems: problems,
	}
}

// PublishCheck is the dry run: the same checks publish enforces, with nothing written.
func (s *Service) PublishCheck(ctx context.Context, c Caller, id string) (*gates.Report, error) {
	if !c.can(rbac.JobEditDraft) && !c.can(rbac.JobPublishExternal) {
		return nil, forbidden("you cannot check jobs for publishing")
	}
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	env, err := s.loadEnv(ctx, c)
	if err != nil {
		return nil, err
	}
	return gates.Check(s.checkInput(rec, env)), nil
}

// TransitionResult is a job after a status change, with the advice the checks produced.
type TransitionResult struct {
	Job      *JobView      `json:"job"`
	Warnings []gates.Issue `json:"warnings"`
}

// Publish publishes a job, or submits it for approval when the caller may not publish.
func (s *Service) Publish(ctx context.Context, c Caller, id string, ifMatch int) (*TransitionResult, error) {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return s.transition(ctx, c, id, lifecycle.PublishAction(s.lc(c), rec.Job.Status), ifMatch)
}

// Transition applies a lifecycle action named in a request (pause, resume, close, reopen, archive, to_draft,
// withdraw). Publishing has its own entry point because who publishes decides between publish and submit.
func (s *Service) Transition(
	ctx context.Context, c Caller, id, action string, ifMatch int,
) (*TransitionResult, error) {
	a, err := lifecycle.Parse(action)
	if err != nil {
		return nil, mapLifecycleError(err)
	}
	if a == lifecycle.ActionPublish || a == lifecycle.ActionSubmit {
		return s.Publish(ctx, c, id, ifMatch)
	}
	return s.transition(ctx, c, id, a, ifMatch)
}

func (s *Service) transition(
	ctx context.Context, c Caller, id string, a lifecycle.Action, ifMatch int,
) (*TransitionResult, error) {
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	plan, err := lifecycle.Authorize(s.lc(c), lifecycle.Subject{Status: rec.Job.Status, CreatedBy: rec.Job.CreatedBy}, a)
	if err != nil {
		return nil, mapLifecycleError(err)
	}
	var env *environment
	if plan.RunsChecks {
		if env, err = s.loadEnv(ctx, c); err != nil {
			return nil, err
		}
	}
	var report *gates.Report
	out, err := s.Store.UpdateJob(ctx, c.OrgID, c.UserID, id, ifMatch,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			// The job may have moved since it was loaded: decide again under the row lock.
			p, perr := lifecycle.Next(cur.Job.Status, a)
			if perr != nil {
				return nil, perr
			}
			sc := &hiringtypes.StatusChange{To: p.To}
			if p.RunsChecks {
				report = gates.Check(s.checkInput(cur, env))
				if !report.OK() {
					return nil, &BlockedError{Report: report}
				}
				sc.FreezeForm = true
				sc.Log = logEntries(report, s.now())
				if cur.Job.PublicID == "" {
					sc.PublicID = s.newPublicID()
				}
				if rerr := s.Ref.RegisterJob(ctx, hiringtypes.RegistryEntry{
					PublicID: firstNonEmpty(cur.Job.PublicID, sc.PublicID), Region: c.Region, OrganizationID: c.OrgID,
					Status: registryStatus(p.To), PublishedAt: s.now(), ValidThrough: cur.Job.ClosingDate,
				}); rerr != nil {
					return nil, fmt.Errorf("draft: register public job: %w", rerr)
				}
			}
			return &hiringtypes.UpdateResult{
				Completed: cur.Job.CompletedSections, Step: cur.Job.CurrentStep, Status: sc,
				AuditAction: "job." + string(a), AuditDiff: map[string]any{"from": cur.Job.Status, "to": p.To},
			}, nil
		})
	if err != nil {
		return nil, mapStoreError(err)
	}
	if !plan.RunsChecks && out.Job.PublicID != "" {
		// Hide or show the job on the public side. The regional status stays the source of truth, so a failure
		// here is logged and the next transition repairs it.
		if rerr := s.Ref.SetRegistryStatus(ctx, out.Job.PublicID, registryStatus(plan.To)); rerr != nil {
			slog.WarnContext(ctx, "draft: registry status not updated", "job", id, "error", rerr)
		}
	}
	cat, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	res := &TransitionResult{Job: s.view(ctx, c, out, cat), Warnings: []gates.Issue{}}
	if report != nil {
		res.Warnings = report.Warnings
	}
	return res, nil
}

// registryStatus maps a job status to the global registry's. A draft is not public either way, so it is
// recorded as paused.
func registryStatus(status string) string {
	switch status {
	case jobs.StatusPublished, jobs.StatusPaused, jobs.StatusClosed, jobs.StatusArchived, jobs.StatusExpired:
		return status
	}
	return jobs.StatusPaused
}

// logEntries writes down what the publisher faced: one row per gate, then the publish itself.
func logEntries(r *gates.Report, _ time.Time) []hiringtypes.LogEntry {
	out := make([]hiringtypes.LogEntry, 0, len(r.Gates)+1)
	for _, g := range r.Gates {
		event := "unconfirmed"
		switch {
		case g.Auto && g.Confirmed:
			event = "auto_passed"
		case g.Auto:
			event = "auto_failed"
		case g.Confirmed:
			event = "confirmed"
		}
		out = append(out, hiringtypes.LogEntry{GateID: g.ID, Severity: g.Severity, Event: event})
	}
	return append(out, hiringtypes.LogEntry{GateID: "system.publish", Severity: gates.SeverityInformational, Event: "published"})
}

func (s *Service) newPublicID() string {
	if s.NewPublicID != nil {
		return s.NewPublicID()
	}
	b := make([]byte, 7)
	if _, err := rand.Read(b); err != nil {
		panic("draft: random: " + err.Error())
	}
	return "j" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// ---- gate confirmations ----

// Compliance lists the gates that apply to a job with where each stands.
func (s *Service) Compliance(ctx context.Context, c Caller, id string) ([]gates.State, error) {
	r, err := s.PublishCheck(ctx, c, id)
	if err != nil {
		return nil, err
	}
	return r.Gates, nil
}

// ConfirmGate records the customer's confirmation (or withdrawal) of one gate. Gates the server answers from
// the job's own data cannot be confirmed by hand, and platform gates never reach a customer.
func (s *Service) ConfirmGate(
	ctx context.Context, c Caller, id, gateID string, confirmed bool, ifMatch int,
) ([]gates.State, error) {
	if !c.can(rbac.JobPublishExternal) && !c.can(rbac.JobJurisdictionSet) {
		return nil, forbidden("you cannot confirm compliance checks")
	}
	rec, err := s.load(ctx, c, id)
	if err != nil {
		return nil, err
	}
	if rec.Job.Status == jobs.StatusArchived {
		return nil, ErrNotEditable
	}
	env, err := s.loadEnv(ctx, c)
	if err != nil {
		return nil, err
	}
	var gate *gates.State
	for _, g := range gates.Check(s.checkInput(rec, env)).Gates {
		if g.ID == gateID {
			g := g
			gate = &g
		}
	}
	switch {
	case gate == nil:
		return nil, invalid(fe("gate_id", "this check does not apply to the job"))
	case gate.Auto:
		return nil, invalid(fe("gate_id", "this check is answered from the job's own details"))
	case gate.Severity == gates.SeverityInformational:
		return nil, invalid(fe("gate_id", "this is information only; there is nothing to confirm"))
	}
	if _, err = s.Store.UpdateJob(ctx, c.OrgID, c.UserID, id, ifMatch,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			event := "confirmed"
			if !confirmed {
				event = "withdrawn"
			}
			return &hiringtypes.UpdateResult{
				Completed: cur.Job.CompletedSections, Step: cur.Job.CurrentStep,
				Confirm:     &hiringtypes.GateConfirm{GateID: gateID, Version: gate.Version, Confirmed: confirmed, Severity: gate.Severity},
				AuditAction: "job.gate." + event, AuditDiff: map[string]any{"gate": gateID, "version": gate.Version},
			}, nil
		}); err != nil {
		return nil, mapStoreError(err)
	}
	return s.Compliance(ctx, c, id)
}
