package draft

import (
	"context"
	"encoding/json"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/rbac"
)

// CurrentDPAVersion is the version of the terms and data processing agreement that counts as current. It is a
// placeholder until counsel's DPA text is final (due 23 Oct); a new version has 30 days' notice, after which
// publishing needs the new acceptance while live jobs keep running.
const CurrentDPAVersion = "2026-10-draft"

// CompanySetup shows the company's publish prerequisites.
func (s *Service) CompanySetup(ctx context.Context, c Caller) (*hiringtypes.CompanySetup, error) {
	if !c.can(rbac.JobCreate) && !c.can(rbac.JobViewAll) && !c.can(rbac.AccountAgreementsAccept) &&
		!c.can(rbac.FormAutomatedScreening) {
		return nil, forbidden("you cannot see the company's publishing requirements")
	}
	out, err := s.Store.CompanySetup(ctx, c.OrgID, CurrentDPAVersion)
	if err != nil {
		return nil, err
	}
	settings, err := s.Store.GetSettings(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	out.AutomatedScreenOn = settings.AutomatedScreeningEnabled
	return out, nil
}

// AcceptDPA records the company's acceptance of the current terms and DPA. Only a Founder or Legal can.
func (s *Service) AcceptDPA(ctx context.Context, c Caller, version string) (*hiringtypes.CompanySetup, error) {
	if !c.can(rbac.AccountAgreementsAccept) {
		return nil, forbidden("only a Founder or Legal can accept the data processing agreement")
	}
	if version != CurrentDPAVersion {
		return nil, invalid(fe("version", "this is not the current version; reload and read it again"))
	}
	if err := s.Store.AcceptDPA(ctx, c.OrgID, c.UserID, version); err != nil {
		return nil, err
	}
	return s.CompanySetup(ctx, c)
}

// PrivacyContactInput is the contact applicants are told to write to.
type PrivacyContactInput struct {
	Email string `json:"privacy_contact_email"`
	DPO   string `json:"dpo_contact"`
}

// SetPrivacyContact stores the privacy contact (and DPO, if any). Publishing needs the email.
func (s *Service) SetPrivacyContact(ctx context.Context, c Caller, in PrivacyContactInput) (*hiringtypes.CompanySetup, error) {
	if !c.can(rbac.FormPrivacyNotice) {
		return nil, forbidden("you cannot change the company's privacy contact")
	}
	email, dpo := strings.TrimSpace(in.Email), strings.TrimSpace(in.DPO)
	var errs []fieldErr
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || utf8.RuneCountInString(email) > 254 {
		errs = append(errs, fe("privacy_contact_email", "enter an email address applicants can write to"))
	}
	if utf8.RuneCountInString(dpo) > 300 {
		errs = append(errs, fe("dpo_contact", "keep this under 300 characters"))
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	if err := s.Store.SetPrivacyContact(ctx, c.OrgID, email, dpo); err != nil {
		return nil, err
	}
	return s.CompanySetup(ctx, c)
}

// DPIAInput is what Legal records about the company's DPIA.
type DPIAInput struct {
	Reference string `json:"reference"` // where the full assessment lives
	Summary   string `json:"summary"`   // what automated screening the DPIA covers
}

// RecordDPIA stores a new version of the company DPIA. Only Legal can (form.automated_screening.enable).
func (s *Service) RecordDPIA(ctx context.Context, c Caller, in DPIAInput) (*hiringtypes.CompanySetup, error) {
	if !c.can(rbac.FormAutomatedScreening) {
		return nil, forbidden("only Legal can record the company's DPIA")
	}
	ref, sum := strings.TrimSpace(in.Reference), strings.TrimSpace(in.Summary)
	var errs []fieldErr
	if ref == "" && sum == "" {
		errs = append(errs, fe("reference", "say where the assessment is kept, or summarise its scope"))
	}
	if utf8.RuneCountInString(ref) > 500 {
		errs = append(errs, fe("reference", "keep this under 500 characters"))
	}
	if utf8.RuneCountInString(sum) > 2000 {
		errs = append(errs, fe("summary", "keep this under 2000 characters"))
	}
	if len(errs) > 0 {
		return nil, invalid(errs...)
	}
	scope, err := json.Marshal(map[string]string{"reference": ref, "summary": sum})
	if err != nil {
		return nil, err
	}
	if _, err = s.Store.RecordDPIA(ctx, c.OrgID, c.UserID, scope); err != nil {
		return nil, err
	}
	return s.CompanySetup(ctx, c)
}

type fieldErr = jobs.FieldError
