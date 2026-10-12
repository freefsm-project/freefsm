package authorization

import (
	"context"
	"errors"
	"net/mail"
	"net/url"
	"strings"

	"github.com/freefsm-project/freefsm/internal/credentials"

	"github.com/jackc/pgx/v5"
)

// IntegrationSettings is a credential-free configuration projection. Revision
// guards the complete reviewed row, including concurrent legacy settings edits.
type IntegrationSettings struct {
	ClearPassword                                              bool
	InvoiceHTML, EstimateHTML                                  string
	Review                                                     string
	Revision                                                   string
	Host                                                       string
	Port                                                       int
	User, From, Copies                                         string
	InvoiceSubject, InvoiceBody, EstimateSubject, EstimateBody string
	PasswordSet                                                bool
	TileURL, GeocoderURL                                       string
	Tracking                                                   bool
}

func integrationPermission(section, action string) (string, Scope, error) {
	switch section {
	case "mail", "maps":
		return "configuration." + section + "." + action, Configuration, nil
	case "open-tracking":
		return "security.open_tracking." + action, Sealed, nil
	}
	return "", "", ErrNotFound
}

func (s *Service) IntegrationSettings(ctx context.Context, a Actor, section string) (IntegrationSettings, error) {
	var v IntegrationSettings
	permission, scope, err := integrationPermission(section, "view")
	if err != nil {
		return v, err
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return v, err
	}
	if !p.Allows(permission, scope) {
		return v, ErrForbidden
	}
	switch section {
	case "mail":
		err = s.Pool.QueryRow(ctx, `SELECT xmin::text,smtp_host,smtp_port,smtp_user,smtp_from,email_auto_cc,invoice_email_subject,invoice_email_body,estimate_email_subject,estimate_email_body,smtp_password<>'' FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&v.Revision, &v.Host, &v.Port, &v.User, &v.From, &v.Copies, &v.InvoiceSubject, &v.InvoiceBody, &v.EstimateSubject, &v.EstimateBody, &v.PasswordSet)
	case "maps":
		err = s.Pool.QueryRow(ctx, `SELECT xmin::text,map_tile_url,geocoder_url FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&v.Revision, &v.TileURL, &v.GeocoderURL)
	default:
		err = s.Pool.QueryRow(ctx, `SELECT xmin::text,email_tracking_enabled FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&v.Revision, &v.Tracking)
	}
	if err == nil && section == "mail" {
		var stored string
		err = s.Pool.QueryRow(ctx, `SELECT smtp_password,invoice_email_html,estimate_email_html FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&stored, &v.InvoiceHTML, &v.EstimateHTML)
		if _, e := credentials.Open(stored); e != nil {
			v.Review = e.Error()
		}
		var legacy int
		if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries WHERE company_id=$1 AND (sender_address IS NULL OR sender_address='') AND state IN ('queued','sending')`, p.CompanyID).Scan(&legacy); e != nil {
			return v, e
		}
		if legacy > 0 {
			v.Review += " Historical queued messages have no recorded sender and require explicit review; create a newly reviewed delivery rather than inventing their history."
		}
	}
	if err == nil && section == "maps" {
		for _, raw := range []*string{&v.TileURL, &v.GeocoderURL} {
			if !safeProviderURL(*raw) {
				*raw = ""
				v.Review = "Saved provider URL requires explicit review; unsupported or credential-bearing URL retained but withheld. Enter a credential-free provider URL to replace it."
			}
		}
	}
	return v, err
}

func safeProviderURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, e := url.Parse(strings.NewReplacer("{s}", "a", "{z}", "0", "{x}", "0", "{y}", "0", "{r}", "").Replace(raw))
	return e == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "http" || u.Scheme == "https")
}

var ErrIntegrationInvalid = errors.New("invalid integration settings")

func (s *Service) SaveIntegrationSettings(ctx context.Context, a Actor, section string, v IntegrationSettings, password string) error {
	permission, scope, err := integrationPermission(section, "manage")
	if err != nil {
		return err
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows(permission, scope) {
			return ErrForbidden
		}
		var revision string
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id,xmin::text FROM company_settings WHERE company_id=$1 FOR UPDATE`, p.CompanyID).Scan(&id, &revision); err != nil {
			return err
		}
		if v.Revision == "" || revision != v.Revision {
			return ErrConflict
		}
		switch section {
		case "mail":
			if v.Host != "" {
				if v.Port < 1 || v.Port > 65535 || strings.ContainsAny(v.Host, "/\r\n @") {
					return ErrIntegrationInvalid
				}
				if _, err := mail.ParseAddress(v.From); err != nil {
					return ErrIntegrationInvalid
				}
			}
			if strings.ContainsAny(v.InvoiceSubject+v.EstimateSubject+v.From, "\r\n") {
				return ErrIntegrationInvalid
			}
			if v.Copies != "" {
				if _, err := mail.ParseAddressList(v.Copies); err != nil {
					return ErrIntegrationInvalid
				}
			}
			if v.ClearPassword && password != "" {
				return ErrIntegrationInvalid
			}
			if v.ClearPassword {
				if _, e := tx.Exec(ctx, `UPDATE company_settings SET smtp_password='' WHERE id=$1`, id); e != nil {
					return e
				}
			} else if password != "" {
				encrypted, e := credentials.Seal(password)
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `UPDATE company_settings SET smtp_password=$2 WHERE id=$1`, id, encrypted); e != nil {
					return e
				}
			} else {
				var existing string
				if e := tx.QueryRow(ctx, `SELECT smtp_password FROM company_settings WHERE id=$1`, id).Scan(&existing); e != nil {
					return e
				}
				plain, e := credentials.Open(existing)
				if e != nil {
					return e
				}
				// Saving under an overlapping key ring rotates without requiring
				// the product user to retrieve or re-enter the write-only secret.
				encrypted, e := credentials.Seal(plain)
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `UPDATE company_settings SET smtp_password=$2 WHERE id=$1`, id, encrypted); e != nil {
					return e
				}
			}
			_, err = tx.Exec(ctx, `UPDATE company_settings SET smtp_host=$2,smtp_port=$3,smtp_user=$4,smtp_from=$5,email_auto_cc=$6,invoice_email_subject=$7,invoice_email_body=$8,estimate_email_subject=$9,estimate_email_body=$10 WHERE id=$1`, id, v.Host, v.Port, v.User, v.From, v.Copies, v.InvoiceSubject, v.InvoiceBody, v.EstimateSubject, v.EstimateBody)
		case "maps":
			for _, raw := range []string{v.TileURL, v.GeocoderURL} {
				if !safeProviderURL(raw) {
					return ErrIntegrationInvalid
				}
			}
			_, err = tx.Exec(ctx, `UPDATE company_settings SET map_tile_url=$2,geocoder_url=$3 WHERE id=$1`, id, v.TileURL, v.GeocoderURL)
		default:
			_, err = tx.Exec(ctx, `UPDATE company_settings SET email_tracking_enabled=$2 WHERE id=$1`, id, v.Tracking)
		}
		if err != nil {
			return err
		}
		if section == "mail" {
			if _, err := tx.Exec(ctx, `UPDATE company_settings SET invoice_email_html=$2,estimate_email_html=$3 WHERE id=$1`, id, v.InvoiceHTML, v.EstimateHTML); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, permission, scope, "company_settings", id, map[string]string{"section": section})
	})
}
