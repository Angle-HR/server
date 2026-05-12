// Package waitlist implements waitlist signup HTTP handlers and persistence.
package waitlist

import (
	"fmt"
	"net/mail"
	"strings"

	"github.com/Angle-HR/server/pkg/model"
)

// WaitlistEntry is a stored waitlist signup.
//
//nolint:revive // Public API name matches the waitlist domain vocabulary.
type WaitlistEntry struct {
	model.BaseModel
	Email       string `json:"email"`
	CompanyName string `json:"company_name"`
	CompanySize string `json:"company_size"`
	Role        string `json:"role"`
}

// JoinRequest is the payload for joining the waitlist.
type JoinRequest struct {
	Email       string `json:"email"`
	CompanyName string `json:"company_name"`
	CompanySize string `json:"company_size"`
	Role        string `json:"role"`
}

// Validate checks required fields and email format.
func (r *JoinRequest) Validate() error {
	r.Email = strings.TrimSpace(r.Email)
	r.CompanyName = strings.TrimSpace(r.CompanyName)
	r.CompanySize = strings.TrimSpace(r.CompanySize)
	r.Role = strings.TrimSpace(r.Role)

	if r.Email == "" {
		return fmt.Errorf("email is required")
	}

	if _, err := mail.ParseAddress(r.Email); err != nil {
		return fmt.Errorf("invalid email format")
	}

	if r.CompanyName == "" {
		return fmt.Errorf("company_name is required")
	}

	if r.CompanySize == "" {
		return fmt.Errorf("company_size is required")
	}

	if r.Role == "" {
		return fmt.Errorf("role is required")
	}

	return nil
}
