package offerdoc

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/Angle-HR/server/internal/hiring/applications"
)

func TestWrite(t *testing.T) {
	at := time.Date(2027, 1, 4, 10, 30, 0, 0, time.UTC)
	d := &applications.DocumentData{
		Offer: applications.Offer{Title: "Offer of employment: Engineer", Body: "Dear Zoë,\n\nWe are pleased to offer you the role — welcome ✓.", Compensation: "£50,000",
			StartDate: "2027-02-01", SignedAt: &at, SignedName: "Zoë Łukasz", DocHash: "abc"},
		Company: "Acme", JobTitle: "Engineer", CandidateName: "Zoë", ConsentHash: "def", SignerIP: "203.0.113.9", SignerAgent: "UA",
		Events: []applications.OfferEvent{{Type: "sent", CreatedAt: at, Data: json.RawMessage(`{}`)}, {Type: "signed", IP: "203.0.113.9", CreatedAt: at}},
	}
	pdf, err := Write(d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) < 1000 {
		t.Fatalf("not a pdf: %d bytes", len(pdf))
	}
}
