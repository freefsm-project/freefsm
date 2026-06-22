package services

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/jung-kurt/gofpdf"
)

func GenerateEstimatePDF(w io.Writer, e *ent.Estimate, customer *ent.Customer, statuses []*ent.Status, cs *ent.CompanySettings) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(true, 15)
	pdf.AddPage()

	items, _ := ParseLineItems(e.LineItems)
	writeHeader(pdf, "ESTIMATE", fmt.Sprintf("%05d", e.ID), cs)
	writeCustomer(pdf, customer)
	writeLineItems(pdf, items, parseTaxRate(e.TaxRate), cs)
	writeNotes(pdf, e.Notes)
	writeStatus(pdf, statusNameForPDF(statuses, e.StatusID))

	if cs.InvoiceFooter != "" {
		pdf.SetY(-30)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(128, 128, 128)
		pdf.Cell(0, 5, cs.InvoiceFooter)
		pdf.Ln(5)
	}

	pdf.Output(w)
}

func GenerateInvoicePDF(w io.Writer, i *ent.Invoice, customer *ent.Customer, statuses []*ent.Status, cs *ent.CompanySettings) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetAutoPageBreak(true, 15)
	pdf.AddPage()

	items, _ := ParseLineItems(i.LineItems)
	payments, _ := ParsePayments(i.Payments)
	writeHeader(pdf, "INVOICE", fmt.Sprintf("%05d", i.ID), cs)
	writeCustomer(pdf, customer)
	writeInvoiceDates(pdf, i)
	writeLineItems(pdf, items, parseTaxRate(i.TaxRate), cs)
	if cs.InvoicePaymentTerms != "" {
		pdf.SetFont("Helvetica", "", 9)
		pdf.SetTextColor(0, 0, 0)
		pdf.Cell(0, 6, "Payment Terms: "+cs.InvoicePaymentTerms)
		pdf.Ln(8)
	}
	writePayments(pdf, payments, cs)
	writeNotes(pdf, i.Notes)
	writeStatus(pdf, statusNameForPDF(statuses, i.StatusID))

	if cs.InvoiceFooter != "" {
		pdf.SetY(-30)
		pdf.SetFont("Helvetica", "I", 8)
		pdf.SetTextColor(128, 128, 128)
		pdf.Cell(0, 5, cs.InvoiceFooter)
		pdf.Ln(5)
	}

	pdf.Output(w)
}

func writeHeader(pdf *gofpdf.Fpdf, docType, number string, cs *ent.CompanySettings) {
	pdf.SetFont("Helvetica", "B", 20)
	pdf.SetTextColor(0, 0, 0)
	if cs != nil && cs.BusinessName != "" {
		pdf.Cell(0, 10, cs.BusinessName)
	} else {
		pdf.Cell(0, 10, "FreeFSM")
	}
	if cs != nil && cs.InvoiceLogoPath != "" {
		pdf.ImageOptions(cs.InvoiceLogoPath, 160, 10, 30, 0, false, gofpdf.ImageOptions{ImageType: ""}, 0, "")
	}
	pdf.Ln(8)
	pdf.SetFont("Helvetica", "", 9)
	if cs != nil {
		addr := strings.TrimSpace(cs.Address)
		if addr != "" {
			loc := strings.TrimSpace(cs.City + " " + cs.State + " " + cs.Zip)
			if loc != "" {
				addr += ", " + loc
			}
			pdf.Cell(0, 5, addr)
			pdf.Ln(5)
		}
		if phone := strings.TrimSpace(cs.Phone); phone != "" {
			pdf.Cell(0, 5, phone)
			pdf.Ln(5)
		}
		if email := strings.TrimSpace(cs.Email); email != "" {
			pdf.Cell(0, 5, email)
			pdf.Ln(5)
		}
	}
	pdf.Ln(2)
	if cs != nil {
		r, g, b := hexToRGB(cs.InvoiceColor)
		pdf.SetDrawColor(r, g, b)
		pdf.CellFormat(190, 1, "", "B", 1, "L", false, 0, "")
		pdf.Ln(4)
	}
	pdf.SetFont("Helvetica", "B", 14)
	pdf.Cell(0, 8, docType+" #"+number)
	pdf.Ln(12)
}

func writeCustomer(pdf *gofpdf.Fpdf, c *ent.Customer) {
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(0, 6, "Bill To:")
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "", 10)
	if c != nil {
		pdf.Cell(0, 5, c.DisplayName)
		if c.CompanyName != "" {
			pdf.Ln(5)
			pdf.Cell(0, 5, c.CompanyName)
		}
		if c.Email != "" {
			pdf.Ln(5)
			pdf.Cell(0, 5, c.Email)
		}
		if c.Phone != "" {
			pdf.Ln(5)
			pdf.Cell(0, 5, c.Phone)
		}
	}
	pdf.Ln(10)
}

func writeInvoiceDates(pdf *gofpdf.Fpdf, i *ent.Invoice) {
	pdf.SetFont("Helvetica", "", 10)
	if !i.InvoiceDate.IsZero() {
		pdf.Cell(0, 5, "Invoice Date: "+i.InvoiceDate.Format("Jan 2, 2006"))
		pdf.Ln(5)
	}
	if !i.DueDate.IsZero() {
		pdf.Cell(0, 5, "Due Date: "+i.DueDate.Format("Jan 2, 2006"))
		pdf.Ln(10)
	}
}

func writeLineItems(pdf *gofpdf.Fpdf, items []LineItem, taxRate float64, cs *ent.CompanySettings) {
	if len(items) == 0 {
		return
	}

	headers := []string{"Item", "Qty", "Price", "Total"}
	widths := []float64{80, 20, 40, 40}
	pdf.SetFont("Helvetica", "B", 9)
	if cs != nil && cs.InvoiceColor != "" {
		r, g, b := hexToRGB(cs.InvoiceColor)
		pdf.SetFillColor(r, g, b)
		pdf.SetTextColor(255, 255, 255)
	} else {
		pdf.SetFillColor(230, 230, 230)
		pdf.SetTextColor(0, 0, 0)
	}
	for i, h := range headers {
		pdf.CellFormat(widths[i], 7, h, "1", 0, "C", true, 0, "")
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(7)

	var subtotal float64
	var taxableSubtotal float64
	pdf.SetFont("Helvetica", "", 9)
	for _, li := range items {
		lineTotal := li.UnitPrice * float64(li.Quantity)
		subtotal += lineTotal
		if li.Taxable {
			taxableSubtotal += lineTotal
		}
		pdf.CellFormat(widths[0], 6, li.Title, "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[1], 6, strconv.Itoa(li.Quantity), "1", 0, "C", false, 0, "")
		pdf.CellFormat(widths[2], 6, fmt.Sprintf("$%.2f", li.UnitPrice), "1", 0, "R", false, 0, "")
		pdf.CellFormat(widths[3], 6, fmt.Sprintf("$%.2f", lineTotal), "1", 0, "R", false, 0, "")
		pdf.Ln(6)
	}

	pdf.Ln(4)
	pdf.SetFont("Helvetica", "B", 10)
	x := pdf.GetX() + 100
	pdf.SetX(x)
	pdf.CellFormat(40, 6, "Subtotal:", "", 0, "R", false, 0, "")
	pdf.CellFormat(40, 6, fmt.Sprintf("$%.2f", subtotal), "", 1, "R", false, 0, "")

	if taxRate > 0 {
		tax := taxableSubtotal * taxRate / 100
		pdf.SetX(x)
		pdf.CellFormat(40, 6, fmt.Sprintf("Tax (%.2f%%):", taxRate), "", 0, "R", false, 0, "")
		pdf.CellFormat(40, 6, fmt.Sprintf("$%.2f", tax), "", 1, "R", false, 0, "")
		subtotal += tax
	}

	pdf.SetX(x)
	pdf.CellFormat(40, 6, "Total:", "T", 0, "R", false, 0, "")
	pdf.CellFormat(40, 6, fmt.Sprintf("$%.2f", subtotal), "T", 1, "R", false, 0, "")
	pdf.Ln(6)
}

func writePayments(pdf *gofpdf.Fpdf, payments []Payment, cs *ent.CompanySettings) {
	if len(payments) == 0 {
		return
	}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Payments")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 9)
	headers := []string{"Date", "Method", "Reference", "Amount"}
	widths := []float64{35, 40, 60, 45}
	if cs != nil {
		r, g, b := hexToRGB(cs.InvoiceColor)
		pdf.SetFillColor(r, g, b)
		pdf.SetTextColor(255, 255, 255)
	} else {
		pdf.SetFillColor(230, 230, 230)
	}
	for i, h := range headers {
		pdf.CellFormat(widths[i], 6, h, "1", 0, "C", true, 0, "")
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(6)
	var total float64
	for _, p := range payments {
		total += p.Amount
		pdf.CellFormat(widths[0], 6, p.Date, "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[1], 6, p.Method, "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[2], 6, p.Reference, "1", 0, "L", false, 0, "")
		pdf.CellFormat(widths[3], 6, fmt.Sprintf("$%.2f", p.Amount), "1", 0, "R", false, 0, "")
		pdf.Ln(6)
	}
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(widths[0]+widths[1]+widths[2], 6, "Total Paid:", "1", 0, "R", false, 0, "")
	pdf.CellFormat(widths[3], 6, fmt.Sprintf("$%.2f", total), "1", 0, "R", false, 0, "")
	pdf.Ln(10)
}

func writeNotes(pdf *gofpdf.Fpdf, notes string) {
	if notes == "" {
		return
	}
	pdf.SetFont("Helvetica", "B", 10)
	pdf.Cell(0, 6, "Notes")
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "", 9)
	pdf.MultiCell(0, 5, notes, "", "L", false)
	pdf.Ln(4)
}

func writeStatus(pdf *gofpdf.Fpdf, status string) {
	if status == "" {
		return
	}
	pdf.SetFont("Helvetica", "I", 9)
	pdf.Cell(0, 6, "Status: "+status)
}

func parseTaxRate(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func statusNameForPDF(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return ""
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.Name
		}
	}
	return ""
}

func hexToRGB(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0
	}
	r, _ := strconv.ParseInt(hex[0:2], 16, 64)
	g, _ := strconv.ParseInt(hex[2:4], 16, 64)
	b, _ := strconv.ParseInt(hex[4:6], 16, 64)
	return int(r), int(g), int(b)
}
