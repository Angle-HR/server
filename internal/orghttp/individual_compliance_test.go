package orghttp

import (
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/onboarding"
)

const (
	testIndustry = "62000000-0000-4000-8000-000000000001"
	testBizType  = "61000000-0000-4000-8000-000000000001"
)

func TestParseCompliance_individualNeedsOnlyTheIndustry(t *testing.T) {
	h := &ProductOnboardingHandler{validate: validator.New()}
	req, btype, ind, err := h.parseCompliance(onboarding.AccountIndividual, productComplianceBody{IndustryID: testIndustry})
	if err != nil || btype != uuid.Nil || ind.String() != testIndustry || req.EmployeeCount != 0 {
		t.Fatalf("req %+v btype %v ind %v err %v", req, btype, ind, err)
	}
	// A client that still sends the business questions is accepted; invalid extras are dropped, not refused.
	req, btype, _, err = h.parseCompliance(onboarding.AccountIndividual,
		productComplianceBody{IndustryID: testIndustry, BusinessTypeID: testBizType, EmployeeCount: 99999})
	if err != nil || btype.String() != testBizType || req.EmployeeCount != 0 {
		t.Fatalf("req %+v btype %v err %v", req, btype, err)
	}
	if _, _, _, err = h.parseCompliance(onboarding.AccountIndividual, productComplianceBody{}); err == nil {
		t.Fatal("the industry is required")
	}
}

func TestParseCompliance_businessStillNeedsEverything(t *testing.T) {
	h := &ProductOnboardingHandler{validate: validator.New()}
	if _, _, _, err := h.parseCompliance(onboarding.AccountBusiness, productComplianceBody{IndustryID: testIndustry}); err == nil {
		t.Fatal("a business sending only industry_id must still be refused")
	}
	_, btype, ind, err := h.parseCompliance(onboarding.AccountBusiness,
		productComplianceBody{IndustryID: testIndustry, BusinessTypeID: testBizType, EmployeeCount: 5})
	if err != nil || btype.String() != testBizType || ind.String() != testIndustry {
		t.Fatalf("btype %v ind %v err %v", btype, ind, err)
	}
}
