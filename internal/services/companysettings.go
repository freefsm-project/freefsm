package services

import (
	"context"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/companysettings"
	"github.com/freefsm-project/freefsm/internal/passwordpolicy"
)

type CompanySettingsService struct {
	client *ent.Client
}

type DocumentDefaults struct {
	Notes, Terms, LineDescription string
	Days                          *int
}

// DateFrom projects a nullable calendar-day offset from an explicit base. The
// caller names the result Invoice Due Date or Estimate Valid Through.
func (d DocumentDefaults) DateFrom(base time.Time) string {
	if d.Days == nil {
		return ""
	}
	return base.AddDate(0, 0, *d.Days).Format("2006-01-02")
}

func ResolveDocumentDefaults(cs *ent.CompanySettings, kind string, customer map[string]*string) DocumentDefaults {
	d := DocumentDefaults{}
	if cs != nil {
		d.LineDescription = cs.DefaultLineDescription
		if kind == "invoice" {
			d.Days = cs.DefaultDueDays
			d.Notes = cs.DefaultInvoiceNotes
			d.Terms = cs.InvoicePaymentTerms
		} else {
			d.Days = cs.EstimateValidDays
			d.Notes = cs.DefaultEstimateNotes
			d.Terms = cs.EstimateTerms
		}
	}
	if value := customer[kind+"_notes"]; value != nil {
		d.Notes = *value
	}
	if value := customer[kind+"_terms"]; value != nil {
		d.Terms = *value
	}
	if value := customer["line_description"]; value != nil {
		d.LineDescription = *value
	}
	daysKey := "invoice_due_days"
	if kind == "estimate" {
		daysKey = "estimate_valid_days"
	}
	if value := customer[daysKey]; value != nil {
		d.Days = nil
		if *value != "" {
			days, err := strconv.Atoi(*value)
			if err == nil {
				d.Days = &days
			}
		}
	}
	return d
}

func NewCompanySettingsService(client *ent.Client) *CompanySettingsService {
	return &CompanySettingsService{client: client}
}

func (s *CompanySettingsService) Get(ctx context.Context) (*ent.CompanySettings, error) {
	return s.client.CompanySettings.Query().First(ctx)
}

// GetForCompany is required by background work, where request tenant context is absent.
func (s *CompanySettingsService) GetForCompany(ctx context.Context, companyID int64) (*ent.CompanySettings, error) {
	return s.client.CompanySettings.Query().Where(companysettings.CompanyIDEQ(companyID)).Only(ctx)
}

type AdministrativeSettingsParams struct {
	SmtpHost                 string
	SmtpPort                 int
	SmtpUser                 string
	SmtpPassword             string
	SmtpFrom                 string
	EmailAutoCC              string
	InvoiceEmailSubject      string
	InvoiceEmailBody         string
	EstimateEmailSubject     string
	EstimateEmailBody        string
	PasswordMinLength        int
	PasswordRequireUppercase bool
	PasswordRequireLowercase bool
	PasswordRequireDigit     bool
	PasswordRequireSpecial   bool
	MapTileURL               string
	GeocoderURL              string
	EmailTrackingEnabled     bool
}

func (s *CompanySettingsService) SaveAdministrativeSettings(ctx context.Context, p AdministrativeSettingsParams) error {
	if request, ok := authorization.FromContext(ctx); ok {
		return request.Service.SavePasswordPolicy(ctx, request.Actor, passwordpolicy.Policy{MinLength: p.PasswordMinLength, Upper: p.PasswordRequireUppercase, Lower: p.PasswordRequireLowercase, Digit: p.PasswordRequireDigit, Special: p.PasswordRequireSpecial})
	}
	cs, err := s.Get(ctx)
	if err != nil {
		return err
	}
	// This legacy sealed editor owns security only. Configuration and
	// integration writes use their independent, revision-guarded boundaries.
	_, err = s.client.CompanySettings.UpdateOne(cs).
		SetPasswordMinLength(p.PasswordMinLength).
		SetPasswordRequireUppercase(p.PasswordRequireUppercase).
		SetPasswordRequireLowercase(p.PasswordRequireLowercase).
		SetPasswordRequireDigit(p.PasswordRequireDigit).
		SetPasswordRequireSpecial(p.PasswordRequireSpecial).
		Save(ctx)
	return err
}
