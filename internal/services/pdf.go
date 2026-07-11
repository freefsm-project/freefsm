package services

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jung-kurt/gofpdf"
)

const (
	pdfMargin = 12.0
	pdfBottom = 18.0
)

type pdfTotals struct {
	Subtotal        float64
	Tax             float64
	Total           float64
	Paid            float64
	Due             float64
	TaxableSubtotal float64
}

func GenerateEstimatePDF(w io.Writer, e *ent.Estimate, customer *ent.Customer, job *ent.Job, statuses []*ent.Status, cs *ent.CompanySettings) error {
	items, err := ParseLineItems(e.LineItems)
	if err != nil {
		return fmt.Errorf("parse estimate line items: %w", err)
	}

	pdf := newDocumentPDF()
	status, statusColor := statusForPDF(statuses, e.StatusID)
	number := documentNumber("estimate", e.ID, cs)
	writeTopHeader(pdf, "ESTIMATE", number, status, statusColor, cs)
	writeDetailRow(pdf, customer, job, nil, estimateDetails(e, status, cs), cs)
	writeDocumentNotes(pdf, e.Notes)
	totals := writeLineItems(pdf, items, parseTaxRate(e.TaxRate), cs)
	writeSummary(pdf, cs, totals, false)

	return pdf.Output(w)
}

func GenerateInvoicePDF(w io.Writer, i *ent.Invoice, customer *ent.Customer, job *ent.Job, asset *ent.Asset, statuses []*ent.Status, cs *ent.CompanySettings) error {
	items, err := ParseLineItems(i.LineItems)
	if err != nil {
		return fmt.Errorf("parse invoice line items: %w", err)
	}
	payments, err := ParsePayments(i.Payments)
	if err != nil {
		return fmt.Errorf("parse invoice payments: %w", err)
	}

	pdf := newDocumentPDF()
	status, statusColor := statusForPDF(statuses, i.StatusID)
	number := FormatInvoiceNumber(i.InvoiceNumber, cs)
	writeTopHeader(pdf, "INVOICE", number, status, statusColor, cs)
	writeDetailRow(pdf, customer, job, asset, invoiceDetails(i, status, cs), cs)
	writeDocumentNotes(pdf, i.Notes)
	totals := writeLineItems(pdf, items, parseTaxRate(i.TaxRate), cs)
	for _, p := range payments {
		totals.Paid += p.Amount
	}
	totals.Due = totals.Total - totals.Paid
	writeSummary(pdf, cs, totals, true)

	return pdf.Output(w)
}

func newDocumentPDF() *gofpdf.Fpdf {
	pdf := gofpdf.New("P", "mm", "Letter", "")
	pdf.SetMargins(pdfMargin, pdfMargin, pdfMargin)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()
	return pdf
}

func writeTopHeader(pdf *gofpdf.Fpdf, docType, number, status, statusColor string, cs *ent.CompanySettings) {
	pageWidth, _ := pdf.GetPageSize()
	accentR, accentG, accentB := accentColor(cs)

	if cs != nil && cs.InvoiceLogoPath != "" {
		if _, err := os.Stat(cs.InvoiceLogoPath); err == nil {
			pdf.ImageOptions(cs.InvoiceLogoPath, pdfMargin, 11, 41.6, 0, false, gofpdf.ImageOptions{ImageType: ""}, 0, "")
		}
	}

	pdf.SetTextColor(0, 0, 0)
	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetXY(55, 12)
	pdf.CellFormat(pageWidth-110, 6, businessNameForPDF(cs), "", 1, "C", false, 0, "")
	pdf.SetFont("Helvetica", "", 8)
	for _, line := range companyDetailLines(cs) {
		pdf.SetX(55)
		pdf.CellFormat(pageWidth-110, 4.5, line, "", 1, "C", false, 0, "")
	}

	pdf.SetXY(pageWidth-62, 12)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.CellFormat(50, 6, docType+" #"+number, "", 2, "R", false, 0, "")
	statusR, statusG, statusB := accentR, accentG, accentB
	if strings.TrimSpace(statusColor) != "" {
		statusR, statusG, statusB = hexToRGB(statusColor)
	}
	writeStatusBadge(pdf, pageWidth-45, 22, status, statusR, statusG, statusB)

	pdf.SetDrawColor(accentR, accentG, accentB)
	pdf.SetLineWidth(0.4)
	pdf.Line(pdfMargin, 42, pageWidth-pdfMargin, 42)
	pdf.SetY(47)
}

func writeStatusBadge(pdf *gofpdf.Fpdf, x, y float64, status string, r, g, b int) {
	if status == "" {
		status = "Draft"
	}
	pdf.SetFillColor(r, g, b)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetXY(x, y)
	pdf.CellFormat(33, 7, strings.ToUpper(status), "", 0, "C", true, 0, "")
	pdf.SetTextColor(0, 0, 0)
}

func writeDetailRow(pdf *gofpdf.Fpdf, customer *ent.Customer, job *ent.Job, asset *ent.Asset, details []string, cs *ent.CompanySettings) {
	pageWidth, _ := pdf.GetPageSize()
	y := pdf.GetY()
	if asset == nil {
		colGap := 6.0
		colWidth := (pageWidth - pdfMargin*2 - colGap*2) / 3
		leftHeight := writeInfoBlock(pdf, pdfMargin, y, colWidth, "Customer", customerLines(customer))
		midHeight := writeInfoBlock(pdf, pdfMargin+colWidth+colGap, y, colWidth, "Job", jobLines(job, cs))
		rightHeight := writeInfoBlock(pdf, pdfMargin+(colWidth+colGap)*2, y, colWidth, "Details", details)
		pdf.SetY(y + maxFloat(leftHeight, midHeight, rightHeight) + 7)
		return
	}

	colGap := 4.0
	colWidth := (pageWidth - pdfMargin*2 - colGap*3) / 4
	heights := []float64{
		writeInfoBlock(pdf, pdfMargin, y, colWidth, "Customer", customerLines(customer)),
		writeInfoBlock(pdf, pdfMargin+colWidth+colGap, y, colWidth, "Job", jobLines(job, cs)),
		writeInfoBlock(pdf, pdfMargin+(colWidth+colGap)*2, y, colWidth, "Assets", assetLines(asset)),
		writeInfoBlock(pdf, pdfMargin+(colWidth+colGap)*3, y, colWidth, "Details", details),
	}
	pdf.SetY(y + maxFloat(heights...) + 7)
}

func writeInfoBlock(pdf *gofpdf.Fpdf, x, y, width float64, title string, lines []string) float64 {
	pdf.SetXY(x, y)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(width, 5, title, "", 2, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 8.5)
	startY := y
	if len(lines) == 0 {
		lines = []string{"-"}
	}
	for _, line := range lines {
		pdf.SetX(x)
		pdf.MultiCell(width, 4.3, line, "", "L", false)
	}
	return pdf.GetY() - startY
}

func writeDocumentNotes(pdf *gofpdf.Fpdf, notes string) {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return
	}
	ensureSpace(pdf, 20, false, nil)
	pdf.SetFont("Helvetica", "B", 9)
	pdf.CellFormat(0, 5, "Notes", "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 8.5)
	pdf.MultiCell(0, 4.5, notes, "", "L", false)
	pdf.Ln(4)
}

func writeLineItems(pdf *gofpdf.Fpdf, items []LineItem, taxRate float64, cs *ent.CompanySettings) pdfTotals {
	totals := pdfTotals{}
	if len(items) == 0 {
		return totals
	}

	showDescriptions := cs != nil && cs.PdfShowLineItemDescriptions
	widths := itemColumnWidths(pdf)
	writeItemTableHeader(pdf, cs)

	for _, li := range items {
		lineTotal := lineItemTotal(li)
		totals.Subtotal += lineTotal
		if li.Taxable {
			totals.TaxableSubtotal += lineTotal
		}

		itemText := li.Title
		if showDescriptions && strings.TrimSpace(li.Description) != "" {
			itemText += "\n" + strings.TrimSpace(li.Description)
		}
		rowHeight := float64(maxLineCount(
			pdf.SplitLines([]byte(itemText), widths[0]-2),
			pdf.SplitLines([]byte(strconv.Itoa(li.Quantity)), widths[1]),
			pdf.SplitLines([]byte(fmt.Sprintf("$%.2f", li.UnitPrice)), widths[2]),
			pdf.SplitLines([]byte(fmt.Sprintf("$%.2f", lineTotal)), widths[3]),
		)) * 4.3
		rowHeight += 2
		if rowHeight < 8 {
			rowHeight = 8
		}
		ensureSpace(pdf, rowHeight, true, func() { writeItemTableHeader(pdf, cs) })

		x, y := pdfMargin, pdf.GetY()
		pdf.SetDrawColor(200, 200, 200)
		for _, width := range widths {
			pdf.Rect(x, y, width, rowHeight, "D")
			x += width
		}

		x = pdfMargin
		pdf.SetFont("Helvetica", "", 8.5)
		pdf.SetXY(x+1, y+1)
		pdf.MultiCell(widths[0]-2, 4.3, itemText, "", "L", false)
		pdf.SetXY(x+widths[0], y)
		writeCenteredRowCell(pdf, widths[1], rowHeight, strconv.Itoa(li.Quantity))
		writeRightRowCell(pdf, widths[2], rowHeight, fmt.Sprintf("$%.2f", li.UnitPrice))
		writeRightRowCell(pdf, widths[3], rowHeight, fmt.Sprintf("$%.2f", lineTotal))
		pdf.SetXY(pdfMargin, y+rowHeight)
	}

	totals.Tax = totals.TaxableSubtotal * taxRate / 100
	totals.Total = totals.Subtotal + totals.Tax
	pdf.Ln(5)
	return totals
}

func writeItemTableHeader(pdf *gofpdf.Fpdf, cs *ent.CompanySettings) {
	ensureSpace(pdf, 9, false, nil)
	widths := itemColumnWidths(pdf)
	headers := []string{"Item", "Qty", "Price", "Total"}
	r, g, b := accentColor(cs)
	pdf.SetFillColor(r, g, b)
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 8.5)
	pdf.SetX(pdfMargin)
	for i, h := range headers {
		pdf.CellFormat(widths[i], 7, h, "1", 0, "C", true, 0, "")
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(7)
}

func itemColumnWidths(pdf *gofpdf.Fpdf) []float64 {
	pageWidth, _ := pdf.GetPageSize()
	contentWidth := pageWidth - pdfMargin*2
	qtyWidth := 20.0
	priceWidth := 32.0
	totalWidth := 34.0
	return []float64{contentWidth - qtyWidth - priceWidth - totalWidth, qtyWidth, priceWidth, totalWidth}
}

func writeCenteredRowCell(pdf *gofpdf.Fpdf, width, rowHeight float64, text string) {
	x, y := pdf.GetX(), pdf.GetY()
	pdf.SetXY(x, y+(rowHeight-4.3)/2)
	pdf.CellFormat(width, 4.3, text, "", 0, "C", false, 0, "")
	pdf.SetXY(x+width, y)
}

func writeRightRowCell(pdf *gofpdf.Fpdf, width, rowHeight float64, text string) {
	x, y := pdf.GetX(), pdf.GetY()
	pdf.SetXY(x, y+(rowHeight-4.3)/2)
	pdf.CellFormat(width-1, 4.3, text, "", 0, "R", false, 0, "")
	pdf.SetXY(x+width, y)
}

func writeSummary(pdf *gofpdf.Fpdf, cs *ent.CompanySettings, totals pdfTotals, includePayments bool) {
	ensureSpace(pdf, 40, false, nil)
	pageWidth, _ := pdf.GetPageSize()
	y := pdf.GetY()
	footerWidth := 92.0
	totalsX := pageWidth - pdfMargin - 70

	if cs != nil && strings.TrimSpace(cs.InvoiceFooter) != "" {
		pdf.SetXY(pdfMargin, y)
		pdf.SetFont("Helvetica", "I", 8.5)
		pdf.SetTextColor(90, 90, 90)
		pdf.MultiCell(footerWidth, 4.5, strings.TrimSpace(cs.InvoiceFooter), "", "L", false)
		pdf.SetTextColor(0, 0, 0)
	}

	pdf.SetXY(totalsX, y)
	writeTotalLine(pdf, totalsX, "Subtotal", totals.Subtotal, false)
	writeTotalLine(pdf, totalsX, "Tax", totals.Tax, false)
	writeTotalLine(pdf, totalsX, "Total", totals.Total, true)
	if includePayments {
		writeTotalLine(pdf, totalsX, "Amount Paid", totals.Paid, false)
		writeTotalLine(pdf, totalsX, "Amount Due", totals.Due, true)
	}
}

func writeTotalLine(pdf *gofpdf.Fpdf, x float64, label string, amount float64, bold bool) {
	style := ""
	border := ""
	if bold {
		style = "B"
		border = "T"
	}
	pdf.SetX(x)
	pdf.SetFont("Helvetica", style, 9)
	pdf.CellFormat(35, 6, label+":", border, 0, "R", false, 0, "")
	pdf.CellFormat(35, 6, fmt.Sprintf("$%.2f", amount), border, 1, "R", false, 0, "")
}

func ensureSpace(pdf *gofpdf.Fpdf, needed float64, repeatHeader bool, afterPageBreak func()) {
	_, pageHeight := pdf.GetPageSize()
	if pdf.GetY()+needed <= pageHeight-pdfBottom {
		return
	}
	pdf.AddPage()
	pdf.SetY(pdfMargin)
	if repeatHeader && afterPageBreak != nil {
		afterPageBreak()
	}
}

func documentNumber(docType string, id int64, cs *ent.CompanySettings) string {
	prefix := ""
	if cs != nil {
		if docType == "invoice" {
			prefix = strings.TrimSpace(cs.InvoicePrefix)
		} else {
			prefix = strings.TrimSpace(cs.EstimatePrefix)
		}
	}
	if prefix == "" {
		return fmt.Sprintf("%05d", id)
	}
	return fmt.Sprintf("%s%05d", prefix, id)
}

func businessNameForPDF(cs *ent.CompanySettings) string {
	if cs != nil && strings.TrimSpace(cs.BusinessName) != "" {
		return strings.TrimSpace(cs.BusinessName)
	}
	return "FreeFSM"
}

func companyDetailLines(cs *ent.CompanySettings) []string {
	if cs == nil {
		return nil
	}
	var lines []string
	addr := strings.TrimSpace(cs.Address)
	loc := strings.TrimSpace(strings.Join(nonEmpty(cs.City, cs.State, cs.Zip), " "))
	if addr != "" && loc != "" {
		lines = append(lines, addr+", "+loc)
	} else if addr != "" || loc != "" {
		lines = append(lines, strings.TrimSpace(addr+" "+loc))
	}
	lines = append(lines, nonEmpty(cs.Phone, cs.Email)...)
	return lines
}

func customerLines(c *ent.Customer) []string {
	if c == nil {
		return nil
	}
	return nonEmpty(c.DisplayName, c.CompanyName, c.Email, c.Phone)
}

func jobLines(j *ent.Job, cs *ent.CompanySettings) []string {
	if j == nil {
		return nil
	}
	lines := nonEmpty(j.JobType, j.Subtitle)
	loc := companySettingsLocation(cs)
	if j.StartTime != nil {
		lines = append(lines, "Start: "+FormatCompanyDate(*j.StartTime, loc, cs))
	}
	if j.DueDate != nil {
		lines = append(lines, "Due: "+FormatCompanyDate(*j.DueDate, loc, cs))
	}
	return lines
}

func assetLines(asset *ent.Asset) []string {
	if asset == nil {
		return nil
	}
	var lines []string
	if strings.TrimSpace(asset.Manufacturer) != "" {
		lines = append(lines, "Manufacturer: "+asset.Manufacturer)
	}
	if strings.TrimSpace(asset.Model) != "" {
		lines = append(lines, "Model: "+asset.Model)
	}
	if strings.TrimSpace(asset.SerialNumber) != "" {
		lines = append(lines, "Serial: "+asset.SerialNumber)
	}
	return lines
}

func invoiceDetails(i *ent.Invoice, status string, cs *ent.CompanySettings) []string {
	var lines []string
	loc := companySettingsLocation(cs)
	if !i.InvoiceDate.IsZero() {
		lines = append(lines, "Invoice Date: "+FormatCompanyDate(i.InvoiceDate, loc, cs))
	}
	if !i.DueDate.IsZero() {
		lines = append(lines, "Due Date: "+FormatCompanyDate(i.DueDate, loc, cs))
	}
	lines = append(lines, "Status: "+statusText(status))
	return lines
}

func estimateDetails(e *ent.Estimate, status string, cs *ent.CompanySettings) []string {
	var lines []string
	if !e.CreatedAt.IsZero() {
		lines = append(lines, "Created Date: "+FormatCompanyDate(e.CreatedAt, companySettingsLocation(cs), cs))
	}
	lines = append(lines, "Status: "+statusText(status))
	return lines
}

func companySettingsLocation(cs *ent.CompanySettings) *time.Location {
	if cs != nil && cs.Timezone != "" {
		if loc, err := time.LoadLocation(cs.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

func statusText(status string) string {
	if strings.TrimSpace(status) == "" {
		return "Draft"
	}
	return strings.TrimSpace(status)
}

func lineItemTotal(li LineItem) float64 {
	return li.UnitPrice*float64(li.Quantity) - li.Discount + li.Surcharge
}

func parseTaxRate(s string) float64 {
	s = strings.TrimSuffix(s, "%")
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func statusForPDF(statuses []*ent.Status, id *int64) (string, string) {
	if id == nil {
		return "", ""
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.Name, s.Color
		}
	}
	return "", ""
}

func accentColor(cs *ent.CompanySettings) (int, int, int) {
	if cs == nil || strings.TrimSpace(cs.InvoiceColor) == "" {
		return 40, 83, 140
	}
	return hexToRGB(cs.InvoiceColor)
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

func nonEmpty(values ...string) []string {
	var lines []string
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, strings.TrimSpace(value))
		}
	}
	return lines
}

func maxFloat(values ...float64) float64 {
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func maxLineCount(lines ...[][]byte) int {
	max := 0
	for _, line := range lines {
		if len(line) > max {
			max = len(line)
		}
	}
	return max
}
