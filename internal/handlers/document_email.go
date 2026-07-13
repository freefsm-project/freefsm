package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

type documentPDF struct {
	Filename      string
	Data          []byte
	Title         string
	Number        string
	CustomerEmail string
	CustomerName  string
	JobName       string
	JobType       string
	JobSubtitle   string
	Date          string
	Archived      bool
}

func documentEmailDefaults(objectType string, doc documentPDF, cs *ent.CompanySettings) (string, string) {
	subject := ""
	body := ""
	if cs != nil {
		switch objectType {
		case "invoice":
			subject = cs.InvoiceEmailSubject
			body = cs.InvoiceEmailBody
		case "estimate":
			subject = cs.EstimateEmailSubject
			body = cs.EstimateEmailBody
		}
	}
	if strings.TrimSpace(subject) == "" {
		subject = defaultDocumentEmailSubject(objectType, doc.Number)
	}
	if strings.TrimSpace(body) == "" {
		body = defaultDocumentEmailBody(objectType)
	}
	values := map[string]string{
		"business_name":   businessName(cs),
		"customer_name":   doc.CustomerName,
		"document_title":  doc.Title,
		"document_number": doc.Number,
		"invoice_number":  doc.Number,
		"estimate_number": doc.Number,
		"job_name":        doc.JobName,
		"job_type":        doc.JobType,
		"job_subtitle":    doc.JobSubtitle,
		"date":            doc.Date,
	}
	return services.RenderEmailTemplate(subject, values), services.RenderEmailTemplate(body, values)
}

func documentJobFields(j *ent.Job) (string, string, string) {
	if j == nil {
		return "", "", ""
	}
	name := j.JobType
	if j.Subtitle != "" {
		name += " - " + j.Subtitle
	}
	return name, j.JobType, j.Subtitle
}

func defaultDocumentEmailSubject(objectType string, number string) string {
	switch objectType {
	case "invoice":
		return fmt.Sprintf("Invoice %s", number)
	case "estimate":
		return fmt.Sprintf("Estimate %s", number)
	default:
		return "Document"
	}
}

func defaultDocumentEmailBody(objectType string) string {
	doc := "document"
	if objectType == "invoice" || objectType == "estimate" {
		doc = objectType
	}
	return fmt.Sprintf("Hello {customer_name},\n\nPlease find your %s {document_number} attached.\n\nThank you,\n{business_name}", doc)
}

func businessName(cs *ent.CompanySettings) string {
	if cs == nil || cs.BusinessName == "" {
		return "FreeFSM"
	}
	return cs.BusinessName
}

func emailAutoCC(cs *ent.CompanySettings) string {
	if cs == nil {
		return ""
	}
	return cs.EmailAutoCc
}

func saveVersionedDocumentPDF(ctx context.Context, fileSvc *services.FileService, companyID int64, objectType string, objectID int64, doc documentPDF, uploadedBy int64) (*ent.File, string, error) {
	ref, err := objectref.Parse(objectType, objectID)
	if err != nil {
		return nil, "", err
	}
	filename := nextVersionedDocumentPDFName(ctx, fileSvc, companyID, ref, doc.Number)
	f, err := fileSvc.CreateBytes(ctx, companyID, ref, filename, "application/pdf", doc.Data, uploadedBy)
	if err != nil {
		return nil, "", err
	}
	return f, filename, nil
}

func nextVersionedDocumentPDFName(ctx context.Context, fileSvc *services.FileService, companyID int64, ref objectref.Ref, base string) string {
	files, err := fileSvc.List(ctx, companyID, ref)
	if err != nil {
		return fmt.Sprintf("%s-v001.pdf", base)
	}
	maxVersion := 0
	prefix := base + "-v"
	for _, f := range files {
		name := f.OriginalName
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".pdf") {
			continue
		}
		versionText := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".pdf")
		version, err := strconv.Atoi(versionText)
		if err == nil && version > maxVersion {
			maxVersion = version
		}
	}
	return fmt.Sprintf("%s-v%03d.pdf", base, maxVersion+1)
}
