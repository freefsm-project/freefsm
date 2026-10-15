package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var ErrConfigurationInput = errors.New("invalid configuration input")

var ErrConfigurationUnavailable = errors.New("company configuration unavailable")

// ConfigurationField is a closed, secret-free presentation and write allowlist.
type ConfigurationField struct{ Name, Label, Kind string }
type ConfigurationSection struct {
	Name, Label string
	Manage      bool
	Fields      []ConfigurationField
	Values      map[string]string
}
type ConfigurationView struct {
	ReviewIssues    []string
	FormAction      string
	EmailDisabled   bool
	LegacyNumbers   []LegacyNumber
	ManageDocuments bool
	Administrator   bool
	Revision        int64
	Sections        []ConfigurationSection
}

type LegacyNumber struct {
	Type       string
	ID, Number int64
	Next       int64
}

func configurationPermission(section, action string) (string, Scope) {
	if section == "offline" {
		return "security.offline." + action, Sealed
	}
	return "configuration." + section + "." + action, Configuration
}

func configurationFields(section string) []ConfigurationField {
	switch section {
	case "offline":
		return []ConfigurationField{{"offline_hours", "Offline authorization hours (0 disables; maximum 72)", "number"}}
	case "company":
		return []ConfigurationField{{"business_name", "Business name", "text"}, {"address", "Address", "text"}, {"city", "City", "text"}, {"state", "State", "text"}, {"zip", "ZIP", "text"}, {"phone", "Phone", "text"}, {"email", "Email", "text"}, {"tax_id", "Tax ID", "text"}, {"timezone", "Timezone", "text"}, {"date_format", "Date format", "text"}, {"invoice_color", "Document color", "color"}, {"invoice_footer", "Document footer", "textarea"}}
	case "documents":
		return []ConfigurationField{{"invoice_prefix", "Invoice prefix", "text"}, {"next_invoice_number", "Next invoice number", "number"}, {"estimate_prefix", "Estimate prefix", "text"}, {"next_estimate_number", "Next estimate number", "number"}, {"lease_prefix", "Lease prefix", "text"}, {"next_lease_number", "Next lease number", "number"}, {"default_tax_rate", "Default tax rate", "text"}, {"default_due_days", "Invoice due offset (days; blank for No Due Date)", "number"}, {"estimate_valid_days", "Estimate validity offset (days, blank for none)", "number"}, {"invoice_payment_terms", "Invoice terms", "textarea"}, {"estimate_terms", "Estimate terms", "textarea"}, {"default_invoice_notes", "Invoice notes", "textarea"}, {"default_estimate_notes", "Estimate notes", "textarea"}, {"default_line_description", "Line description", "textarea"}, {"pdf_show_line_item_descriptions", "Show line descriptions on PDFs", "checkbox"}}
	}
	return nil
}

func (s *Service) Configuration(ctx context.Context, a Actor) (ConfigurationView, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return ConfigurationView{}, err
	}
	v := ConfigurationView{Administrator: p.Protected, FormAction: "/settings"}
	v.ManageDocuments = p.Allows("configuration.documents.manage", Configuration)
	if p.Allows("configuration.documents.view", Configuration) {
		rows, err := s.Pool.Query(ctx, `SELECT 'invoice',d.id,coalesce(d.invoice_number,0),cs.next_invoice_number FROM invoices d JOIN company_settings cs ON cs.company_id=d.company_id WHERE d.company_id=$1 AND d.number_label='' UNION ALL SELECT 'estimate',d.id,0,cs.next_estimate_number FROM estimates d JOIN company_settings cs ON cs.company_id=d.company_id WHERE d.company_id=$1 AND d.number_label='' ORDER BY 1,2`, p.CompanyID)
		if err != nil {
			return v, err
		}
		defer rows.Close()
		for rows.Next() {
			var n LegacyNumber
			if err := rows.Scan(&n.Type, &n.ID, &n.Number, &n.Next); err != nil {
				return v, err
			}
			v.LegacyNumbers = append(v.LegacyNumbers, n)
		}
		if err := rows.Err(); err != nil {
			return v, err
		}
	}
	for _, name := range []string{"company", "documents", "offline"} {
		permission, scope := configurationPermission(name, "view")
		if !p.Allows(permission, scope) {
			continue
		}
		permission, scope = configurationPermission(name, "manage")
		section := ConfigurationSection{Name: name, Label: strings.ToUpper(name[:1]) + name[1:], Manage: p.Allows(permission, scope), Fields: configurationFields(name), Values: map[string]string{}}
		v.Sections = append(v.Sections, section)
	}
	if len(v.Sections) == 0 {
		return v, ErrForbidden
	}
	// Project only the approved fields, never fetch credentials into a view model.
	keys := []string{}
	for _, section := range v.Sections {
		for _, f := range section.Fields {
			keys = append(keys, fmt.Sprintf("'%s',%s", f.Name, pgx.Identifier{f.Name}.Sanitize()))
		}
	}
	var data []byte
	err = s.Pool.QueryRow(ctx, `SELECT configuration_revision,jsonb_build_object(`+strings.Join(keys, ",")+`) FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&v.Revision, &data)
	if err != nil {
		return v, err
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &values); err != nil {
		return v, err
	}
	for i := range v.Sections {
		for fieldIndex, f := range v.Sections[i].Fields {
			raw := values[f.Name]
			value := string(raw)
			if value == "null" {
				value = ""
			} else if len(raw) > 0 && raw[0] == '"' {
				if err := json.Unmarshal(raw, &value); err != nil {
					return v, err
				}
			}
			v.Sections[i].Values[f.Name] = value
			if _, err := validateConfigurationField(f.Name, value); err != nil {
				v.ReviewIssues = append(v.ReviewIssues, f.Label)
				// A color input normalizes unknown legacy values in the browser.
				// Keep the raw value visible until explicitly reviewed instead.
				if f.Kind == "color" {
					v.Sections[i].Fields[fieldIndex].Kind = "text"
				}
			}
		}
	}
	return v, nil
}

func validateConfigurationField(name, value string) (any, error) {
	invalid := func() (any, error) { return nil, fmt.Errorf("%w: %s", ErrConfigurationInput, name) }
	if !utf8.ValidString(value) {
		return invalid()
	}
	switch name {
	case "offline_hours":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 72 {
			return invalid()
		}
		return n, nil
	case "business_name":
		if strings.TrimSpace(value) == "" {
			return invalid()
		}
	case "timezone":
		if _, err := time.LoadLocation(value); err != nil || value == "" || value == "Local" {
			return invalid()
		}
	case "invoice_color":
		if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(value) {
			return invalid()
		}
	case "date_format":
		if value != "January 2, 2006" && value != "Jan 2, 2006" && value != "2006-01-02" && value != "02/01/2006" && value != "01/02/2006" {
			return invalid()
		}
	case "default_tax_rate":
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%")), 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
			return invalid()
		}
	case "invoice_prefix", "estimate_prefix", "lease_prefix":
		if !validNumberText(value) {
			return invalid()
		}
		return strings.TrimSpace(value), nil
	case "next_invoice_number", "next_estimate_number", "next_lease_number", "default_due_days", "estimate_valid_days":
		if (name == "estimate_valid_days" || name == "default_due_days") && value == "" {
			return nil, nil
		}
		n, err := strconv.ParseInt(value, 10, 64)
		sequence := strings.HasPrefix(name, "next_")
		if err != nil || (sequence && (n < 1 || n == math.MaxInt64)) || (!sequence && (n < math.MinInt32 || n > math.MaxInt32)) {
			return invalid()
		}
		if !sequence {
			year := time.Now().UTC().AddDate(0, 0, int(n)).Year()
			if year < 1 || year > 9999 {
				return invalid()
			}
		}
		return n, nil
	case "pdf_show_line_item_descriptions":
		if value != "true" && value != "false" {
			return invalid()
		}
		return value == "true", nil
	}
	return value, nil
}

func validNumberText(value string) bool {
	return utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// SaveConfiguration shares the authority mutex and row lock with commit-time
// authorization and numbering; the immutable audit is part of the same commit.
func (s *Service) SaveConfiguration(ctx context.Context, a Actor, section string, revision int64, values url.Values) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission, scope := configurationPermission(section, "manage")
		if !p.Allows(permission, scope) {
			return ErrForbidden
		}
		allowed := map[string]bool{"csrf_token": true, "revision": true, "section": true}
		for _, field := range configurationFields(section) {
			allowed[field.Name] = true
		}
		for key, submitted := range values {
			if !allowed[key] || len(submitted) != 1 {
				return ErrConfigurationInput
			}
		}
		var current, next, id int64
		if err := tx.QueryRow(ctx, `SELECT id,configuration_revision,next_invoice_number FROM company_settings WHERE company_id=$1 FOR UPDATE`, p.CompanyID).Scan(&id, &current, &next); err != nil {
			return err
		}
		if revision != current {
			return ErrConflict
		}
		if section == "documents" && (values.Has("invoice_prefix") || values.Has("estimate_prefix") || values.Has("next_invoice_number") || values.Has("next_estimate_number")) {
			var unresolved bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices WHERE company_id=$1 AND number_label='' UNION ALL SELECT 1 FROM estimates WHERE company_id=$1 AND number_label='')`, p.CompanyID).Scan(&unresolved); err != nil {
				return err
			}
			if unresolved {
				return ErrConflict
			}
		}
		sets := []string{}
		args := []any{id}
		changed := []string{}
		for _, f := range configurationFields(section) {
			if !values.Has(f.Name) {
				continue
			}
			value, err := validateConfigurationField(f.Name, values.Get(f.Name))
			if err != nil {
				return err
			}
			if f.Name == "next_invoice_number" && value.(int64) < next {
				return fmt.Errorf("%w: numbering cannot move backwards", ErrConfigurationInput)
			}
			if f.Name == "next_estimate_number" || f.Name == "next_lease_number" {
				var floor int64
				if err := tx.QueryRow(ctx, `SELECT `+pgx.Identifier{f.Name}.Sanitize()+` FROM company_settings WHERE id=$1`, id).Scan(&floor); err != nil {
					return err
				}
				if value.(int64) < floor {
					return ErrConfigurationInput
				}
			}
			args = append(args, value)
			sets = append(sets, fmt.Sprintf("%s=$%d", pgx.Identifier{f.Name}.Sanitize(), len(args)))
			changed = append(changed, f.Name)
		}
		if len(sets) == 0 {
			return ErrConfigurationInput
		}
		if _, err := tx.Exec(ctx, `UPDATE company_settings SET `+strings.Join(sets, ",")+`,updated_at=clock_timestamp() WHERE id=$1`, args...); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, permission, scope, "company_settings", id, map[string]any{"fields": changed, "reviewed_revision": revision})
	})
}

func (s *Service) SaveCompanyLogo(ctx context.Context, a Actor, revision int64, path string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("configuration.company.manage", Configuration) {
			return ErrForbidden
		}
		var id, current int64
		if err := tx.QueryRow(ctx, `SELECT id,configuration_revision FROM company_settings WHERE company_id=$1 FOR UPDATE`, p.CompanyID).Scan(&id, &current); err != nil {
			return err
		}
		if current != revision {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE company_settings SET invoice_logo_path=$2,updated_at=clock_timestamp() WHERE id=$1`, id, path); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "configuration.company.manage", Configuration, "company_settings", id, map[string]any{"fields": []string{"invoice_logo_path"}, "reviewed_revision": revision})
	})
}
