// Package offerdoc writes the signed offer as a PDF: the letter as it was sent, the signature, and the audit trail
// that backs it.
package offerdoc

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"

	"github.com/Angle-HR/server/internal/hiring/applications"
)

// Write makes the PDF of a signed offer.
func Write(d *applications.DocumentData) ([]byte, error) {
	p := fpdf.New("P", "mm", "A4", "")
	p.SetMargins(20, 20, 20)
	p.SetAutoPageBreak(true, 20)
	tr := p.UnicodeTranslatorFromDescriptor("") // core fonts are Latin-1: other characters become their closest match or ?
	p.SetTitle(tr(d.Offer.Title), true)
	p.AddPage()

	p.SetFont("Helvetica", "B", 16)
	p.MultiCell(0, 8, tr(d.Offer.Title), "", "L", false)
	p.SetFont("Helvetica", "", 10)
	p.SetTextColor(90, 90, 90)
	p.MultiCell(0, 5, tr(fmt.Sprintf("%s  |  %s  |  %s", d.Company, d.JobTitle, d.CandidateName)), "", "L", false)
	p.SetTextColor(0, 0, 0)
	p.Ln(4)

	p.SetFont("Helvetica", "", 11)
	p.MultiCell(0, 5.5, tr(d.Offer.Body), "", "L", false)
	p.Ln(3)
	if d.Offer.Compensation != "" {
		p.MultiCell(0, 5.5, tr("Compensation: "+d.Offer.Compensation), "", "L", false)
	}
	if d.Offer.StartDate != "" {
		p.MultiCell(0, 5.5, tr("Start date: "+d.Offer.StartDate), "", "L", false)
	}

	p.Ln(6)
	p.SetFont("Helvetica", "B", 12)
	p.CellFormat(0, 7, "Signature", "", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "I", 16)
	signed := ""
	if d.Offer.SignedAt != nil {
		signed = d.Offer.SignedAt.UTC().Format("2 January 2006, 15:04 UTC")
	}
	p.CellFormat(0, 10, tr(d.Offer.SignedName), "B", 1, "L", false, 0, "")
	p.SetFont("Helvetica", "", 10)
	p.MultiCell(0, 5, tr("Signed electronically on "+signed), "", "L", false)

	p.AddPage()
	p.SetFont("Helvetica", "B", 12)
	p.CellFormat(0, 8, "Audit trail", "", 1, "L", false, 0, "")
	p.SetFont("Courier", "", 8.5)
	lines := []string{
		"Document SHA-256:   " + d.Offer.DocHash,
		"Consent text hash:  " + d.ConsentHash + " (" + applications.ConsentVersion + ")",
		"Signer IP address:  " + d.SignerIP,
		"Signer user agent:  " + d.SignerAgent,
		"",
	}
	for _, e := range d.Events {
		lines = append(lines, fmt.Sprintf("%s  %-9s %s", e.CreatedAt.UTC().Format("2006-01-02 15:04:05"), e.Type, e.IP))
	}
	p.MultiCell(0, 4.5, tr(strings.Join(lines, "\n")), "", "L", false)
	p.Ln(4)
	p.SetFont("Helvetica", "", 9)
	p.MultiCell(0, 4.5, tr("Consent: "+applications.ConsentText), "", "L", false)

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, fmt.Errorf("offerdoc: %w", err)
	}
	return buf.Bytes(), nil
}
