package services

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/companysettings"
	"github.com/jackc/pgx/v5"
)

type CommercialInput struct {
	authorization.CommercialIntent
	Title        *string           `json:"title,omitempty"`
	Notes        *string           `json:"notes,omitempty"`
	Terms        *string           `json:"terms,omitempty"`
	TaxRate      *string           `json:"tax_rate,omitempty"`
	InvoiceDate  *string           `json:"invoice_date,omitempty"`
	DueDate      *string           `json:"due_date,omitempty"`
	ValidThrough *string           `json:"valid_through,omitempty"`
	LineItems    *[]CommercialLine `json:"line_items,omitempty"`
	CustomFields json.RawMessage   `json:"custom_fields,omitempty"`
}

// USD values cross the public boundary as exact two-decimal strings. The
// existing commercial calculator remains the single arithmetic authority.
type CommercialLine struct {
	ItemID      int64   `json:"item_id,omitempty"`
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`
	Quantity    float64 `json:"quantity"`
	UnitPrice   string  `json:"unit_price"`
	Taxable     bool    `json:"taxable"`
	TaxRate     string  `json:"tax_rate,omitempty"`
	Discount    string  `json:"discount,omitempty"`
	Surcharge   string  `json:"surcharge,omitempty"`
}

var commercialMoneyPattern = regexp.MustCompile(`^-?[0-9]+\.[0-9]{2}$`)

func PublicCommercialLines(raw string) ([]CommercialLine, error) {
	lines, err := DecodeLineItems(raw)
	if err != nil {
		return nil, err
	}
	out := make([]CommercialLine, 0, len(lines))
	for _, line := range lines {
		description := line.Description
		out = append(out, CommercialLine{ItemID: line.ItemID, Title: line.Title, Description: &description, Quantity: line.Quantity, UnitPrice: fmt.Sprintf("%.2f", line.UnitPrice), Taxable: line.Taxable, TaxRate: line.TaxRate, Discount: fmt.Sprintf("%.2f", line.Discount), Surcharge: fmt.Sprintf("%.2f", line.Surcharge)})
	}
	return out, nil
}

func commercialLines(input *[]CommercialLine) (*[]LineItem, error) {
	if input == nil {
		return nil, nil
	}
	out := make([]LineItem, 0, len(*input))
	money := func(value string, optional bool) (float64, error) {
		if value == "" && optional {
			return 0, nil
		}
		if !commercialMoneyPattern.MatchString(value) {
			return 0, authorization.ErrCommercialInput
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, authorization.ErrCommercialInput
		}
		return v, nil
	}
	for _, line := range *input {
		price, err := money(line.UnitPrice, false)
		if err != nil {
			return nil, err
		}
		discount, err := money(line.Discount, true)
		if err != nil {
			return nil, err
		}
		surcharge, err := money(line.Surcharge, true)
		if err != nil {
			return nil, err
		}
		description := ""
		if line.Description != nil {
			description = *line.Description
		}
		out = append(out, LineItem{ItemID: line.ItemID, Title: line.Title, Description: description, Quantity: line.Quantity, UnitPrice: price, Taxable: line.Taxable, TaxRate: line.TaxRate, Discount: discount, Surcharge: surcharge})
	}
	return &out, nil
}

func commercialDate(value *string, required bool) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if *value == "" && !required {
		t := time.Time{}
		return &t, nil
	}
	t, err := time.Parse("2006-01-02", *value)
	if err != nil || t.IsZero() {
		return nil, authorization.ErrCommercialInput
	}
	return &t, nil
}

// SaveCommercialAPI is a transport adapter over the same committing details
// boundary used by web forms. API anchors are exclusively the reviewed Job.
func SaveCommercialAPI(ctx context.Context, kind string, id int64, in CommercialInput) (authorization.CommercialReceipt, error) {
	r, ok := authorization.FromContext(ctx)
	if !ok {
		return authorization.CommercialReceipt{}, authorization.ErrForbidden
	}
	intent, err := json.Marshal(in)
	if err != nil {
		return authorization.CommercialReceipt{}, err
	}
	return r.Service.CommercialOperation(ctx, r.Actor, kind, id, in.CommercialIntent, intent, func(ctx context.Context, tx pgx.Tx, customer int64) (int64, error) {
		ctx = context.WithValue(ctx, detailsTxKey{}, tx)
		client := ent.NewClient(ent.Driver(&fieldTxDriver{tx: tx}))
		linesInput, err := commercialLines(in.LineItems)
		if err != nil {
			return 0, err
		}
		if (id == 0 && in.Title == nil) || (in.Title != nil && strings.TrimSpace(*in.Title) == "") {
			return 0, authorization.ErrCommercialInput
		}
		date, err := commercialDate(in.InvoiceDate, true)
		if err != nil {
			return 0, err
		}
		due, err := commercialDate(in.DueDate, false)
		if err != nil {
			return 0, err
		}
		valid, err := commercialDate(in.ValidThrough, false)
		if err != nil {
			return 0, err
		}
		if kind == "estimate" && (date != nil || due != nil) || kind == "invoice" && valid != nil {
			return 0, authorization.ErrCommercialInput
		}
		var fields *string
		if in.CustomFields != nil {
			value := string(in.CustomFields)
			fields = &value
		}
		if id == 0 {
			principal, err := r.Service.Principal(ctx, r.Actor)
			if err != nil {
				return 0, err
			}
			cs, err := client.CompanySettings.Query().Where(companysettings.CompanyIDEQ(principal.CompanyID)).Only(ctx)
			if err != nil {
				return 0, err
			}
			customerDefaults, err := r.Service.DocumentDefaultInputs(ctx, r.Actor, kind, customer, in.JobID)
			if err != nil {
				return 0, err
			}
			defaults := ResolveDocumentDefaults(cs, kind, customerDefaults)
			pick := func(value *string, fallback string) string {
				if value != nil {
					return *value
				}
				return fallback
			}
			notes, terms, tax := pick(in.Notes, defaults.Notes), pick(in.Terms, defaults.Terms), pick(in.TaxRate, cs.DefaultTaxRate)
			var lines []LineItem
			if linesInput != nil {
				lines = *linesInput
				for index := range lines {
					if (*in.LineItems)[index].Description == nil {
						lines[index].Description = defaults.LineDescription
					}
				}
			}
			if _, err := CalculateTotals(lines, tax); err != nil {
				return 0, fmt.Errorf("%w: %v", authorization.ErrCommercialInput, err)
			}
			today, _ := time.Parse("2006-01-02", time.Now().In(companySettingsLocation(cs)).Format("2006-01-02"))
			values := "[]"
			if fields != nil {
				values = *fields
			}
			if kind == "estimate" {
				if valid == nil && defaults.Days != nil {
					d := today.AddDate(0, 0, *defaults.Days)
					valid = &d
				}
				if valid != nil && valid.IsZero() {
					valid = nil
				}
				v, err := saveDetails(ctx, kind, 0, in.JobID, &values, func(ctx context.Context, c *ent.Client, fields string) (*ent.Estimate, int64, error) {
					v, err := NewEstimateService(c).Create(ctx, EstimateCreateParams{CustomerID: customer, JobID: in.JobID, Title: *in.Title, Notes: notes, Terms: terms, TaxRate: tax, LineItems: lines, CustomFields: fields, ValidThrough: valid})
					if err != nil {
						return nil, 0, err
					}
					return v, v.ID, nil
				})
				if err != nil {
					return 0, err
				}
				return v.ID, nil
			}
			if date == nil {
				date = &today
			}
			if due == nil {
				d := time.Time{}
				if defaults.Days != nil {
					d = date.AddDate(0, 0, *defaults.Days)
				}
				due = &d
			}
			v, err := saveDetails(ctx, kind, 0, in.JobID, &values, func(ctx context.Context, c *ent.Client, fields string) (*ent.Invoice, int64, error) {
				v, err := NewInvoiceService(c).Create(ctx, InvoiceCreateParams{CustomerID: customer, JobID: in.JobID, Title: *in.Title, Notes: notes, Terms: terms, TaxRate: tax, LineItems: lines, CustomFields: fields, InvoiceDate: *date, DueDate: *due})
				if err != nil {
					return nil, 0, err
				}
				return v, v.ID, nil
			})
			if err != nil {
				return 0, err
			}
			return v.ID, nil
		}
		if in.TaxRate != nil || linesInput != nil {
			var oldTax, raw string
			if err := tx.QueryRow(ctx, `SELECT tax_rate,line_items::text FROM `+kind+`s WHERE id=$1`, id).Scan(&oldTax, &raw); err != nil {
				return 0, err
			}
			if in.TaxRate != nil {
				oldTax = *in.TaxRate
			}
			input, err := r.Service.DocumentCustomer(ctx, r.Actor, customer, in.JobID, oldTax)
			if err != nil {
				return 0, err
			}
			in.TaxRate = &input.TaxRate
			lines, err := DecodeLineItems(raw)
			if err != nil {
				return 0, err
			}
			if linesInput != nil {
				lines = *linesInput
			}
			if _, err := CalculateTotals(lines, input.TaxRate); err != nil {
				return 0, fmt.Errorf("%w: %v", authorization.ErrCommercialInput, err)
			}
		}
		if kind == "estimate" {
			clear := valid != nil && valid.IsZero()
			if clear {
				valid = nil
			}
			_, err := NewEstimateService(client).SaveDetails(ctx, id, EstimateUpdateParams{Title: in.Title, Notes: in.Notes, Terms: in.Terms, TaxRate: in.TaxRate, LineItems: linesInput, CustomFields: fields, ValidThrough: valid, ClearValidThrough: clear})
			return id, err
		}
		_, err = NewInvoiceService(client).SaveDetails(ctx, id, InvoiceUpdateParams{Title: in.Title, Notes: in.Notes, Terms: in.Terms, TaxRate: in.TaxRate, LineItems: linesInput, CustomFields: fields, InvoiceDate: date, DueDate: due})
		return id, err
	})
}
