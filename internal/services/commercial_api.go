package services

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/companysettings"
	"github.com/jackc/pgx/v5"
)

type CommercialInput struct {
	authorization.CommercialIntent
	Adjustments  *DocumentAdjustments `json:"adjustments,omitempty"`
	Title        *string              `json:"title,omitempty"`
	Notes        *string              `json:"notes,omitempty"`
	Terms        *string              `json:"terms,omitempty"`
	TaxRate      *string              `json:"tax_rate,omitempty"`
	InvoiceDate  *string              `json:"invoice_date,omitempty"`
	DueDate      *string              `json:"due_date,omitempty"`
	ValidThrough *string              `json:"valid_through,omitempty"`
	LineItems    *[]CommercialLine    `json:"line_items,omitempty"`
	CustomFields json.RawMessage      `json:"custom_fields,omitempty"`
}

// USD values and quantities retain their exact decimal representation across
// transport and persistence. Versioned commercial acceptance owns arithmetic;
// float fields in older line captures are never the new monetary authority.
type CommercialLine struct {
	ItemID              int64                  `json:"item_id,omitempty"`
	Title               string                 `json:"title"`
	Description         *string                `json:"description,omitempty"`
	Quantity            commercial.Decimal     `json:"quantity"`
	UnitPrice           string                 `json:"unit_price"`
	Taxable             bool                   `json:"taxable"`
	TaxRate             string                 `json:"tax_rate,omitempty"`
	Discount            string                 `json:"discount,omitempty"`
	Surcharge           string                 `json:"surcharge,omitempty"`
	DiscountAdjustment  *commercial.Adjustment `json:"discount_adjustment,omitempty"`
	SurchargeAdjustment *commercial.Adjustment `json:"surcharge_adjustment,omitempty"`
}

var commercialMoneyPattern = regexp.MustCompile(`^-?[0-9]+\.[0-9]{2}$`)

func PublicCommercialLines(raw string) ([]CommercialLine, error) {
	lines, err := DecodeLineItems(raw)
	if err != nil {
		return nil, err
	}
	out := make([]CommercialLine, 0, len(lines))
	captured, captureErr := commercial.CapturedInput(raw, "0", nil, nil)
	for index, line := range lines {
		description := line.Description
		qty := line.ExactQuantity
		if qty == "" {
			qty = strconv.FormatFloat(line.Quantity, 'f', -1, 64)
		}
		price := line.ExactUnitPrice
		if price == "" {
			price = decimalMoney(strconv.FormatFloat(line.UnitPrice, 'f', -1, 64))
		}
		if captureErr == nil {
			price = captured.Lines[index].UnitPrice
			qty = captured.Lines[index].Quantity
		}
		discount, surcharge := decimalMoney(strconv.FormatFloat(line.Discount, 'f', -1, 64)), decimalMoney(strconv.FormatFloat(line.Surcharge, 'f', -1, 64))
		if captureErr == nil {
			if a := captured.Lines[index].Discount; line.DiscountAdjustment == nil && a != nil {
				discount = a.Value
			}
			if a := captured.Lines[index].Surcharge; line.SurchargeAdjustment == nil && a != nil {
				surcharge = a.Value
			}
		}
		out = append(out, CommercialLine{ItemID: line.ItemID, Title: line.Title, Description: &description, Quantity: commercial.NewDecimalString(qty), UnitPrice: price, Taxable: line.Taxable, TaxRate: line.TaxRate, Discount: discount, Surcharge: surcharge, DiscountAdjustment: line.DiscountAdjustment, SurchargeAdjustment: line.SurchargeAdjustment})
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
		for _, a := range []*commercial.Adjustment{line.DiscountAdjustment, line.SurchargeAdjustment} {
			if commercial.ValidateAdjustment(a) != nil {
				return nil, authorization.ErrCommercialInput
			}
		}
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
		qty, err := line.Quantity.Float64()
		if err != nil {
			// Exact quantities are not bounded by the compatibility float field.
			qty = 0
		}
		d, s := line.DiscountAdjustment, line.SurchargeAdjustment
		if discount != 0 {
			if d != nil {
				return nil, authorization.ErrCommercialInput
			}
			d = &commercial.Adjustment{Kind: "fixed", Value: line.Discount}
		}
		if surcharge != 0 {
			if s != nil {
				return nil, authorization.ErrCommercialInput
			}
			s = &commercial.Adjustment{Kind: "fixed", Value: line.Surcharge}
		}
		out = append(out, LineItem{ItemID: line.ItemID, Title: line.Title, Description: description, Quantity: qty, UnitPrice: price, Taxable: line.Taxable, TaxRate: line.TaxRate, ExactUnitPrice: line.UnitPrice, ExactQuantity: line.Quantity.String(), DiscountAdjustment: d, SurchargeAdjustment: s})
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
	return r.Service.CommercialOperation(ctx, r.Actor, kind, id, in.CommercialIntent, intent, func() ([]byte, error) { return legacyCommercialIntent(in, intent) }, func(ctx context.Context, tx pgx.Tx, customer int64) (int64, error) {
		ctx = context.WithValue(ctx, detailsTxKey{}, tx)
		client := ent.NewClient(ent.Driver(&fieldTxDriver{tx: tx}))
		linesInput, err := commercialLines(in.LineItems)
		if err != nil {
			return 0, err
		}
		adjustments := DocumentAdjustments{}
		if in.Adjustments != nil {
			adjustments = *in.Adjustments
		}
		for _, a := range []*commercial.Adjustment{adjustments.Discount, adjustments.Surcharge} {
			if commercial.ValidateAdjustment(a) != nil {
				return 0, authorization.ErrCommercialInput
			}
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
					v, err := NewEstimateService(c).Create(ctx, EstimateCreateParams{Adjustments: adjustments, CustomerID: customer, JobID: in.JobID, Title: *in.Title, Notes: notes, Terms: terms, TaxRate: tax, LineItems: lines, CustomFields: fields, ValidThrough: valid})
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
				v, err := NewInvoiceService(c).Create(ctx, InvoiceCreateParams{Adjustments: adjustments, CustomerID: customer, JobID: in.JobID, Title: *in.Title, Notes: notes, Terms: terms, TaxRate: tax, LineItems: lines, CustomFields: fields, InvoiceDate: *date, DueDate: *due})
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
		if kind == "estimate" {
			clear := valid != nil && valid.IsZero()
			if clear {
				valid = nil
			}
			_, err := NewEstimateService(client).SaveDetails(ctx, id, EstimateUpdateParams{Adjustments: in.Adjustments, Title: in.Title, Notes: in.Notes, Terms: in.Terms, TaxRate: in.TaxRate, LineItems: linesInput, CustomFields: fields, ValidThrough: valid, ClearValidThrough: clear})
			return id, err
		}
		_, err = NewInvoiceService(client).SaveDetails(ctx, id, InvoiceUpdateParams{Adjustments: in.Adjustments, Title: in.Title, Notes: in.Notes, Terms: in.Terms, TaxRate: in.TaxRate, LineItems: linesInput, CustomFields: fields, InvoiceDate: date, DueDate: due})
		return id, err
	})
}

// Preserve the installed float-normalized intent only when authorization has
// found that immutable historical operation. New commands retain exact digits.
func legacyCommercialIntent(in CommercialInput, current []byte) ([]byte, error) {
	if in.LineItems == nil {
		return current, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(current, &fields); err != nil {
		return nil, err
	}
	var lines []map[string]json.RawMessage
	if err := json.Unmarshal(fields["line_items"], &lines); err != nil {
		return nil, err
	}
	for index, line := range *in.LineItems {
		quantity, err := line.Quantity.LegacyJSON()
		if err != nil {
			return nil, err
		}
		lines[index]["quantity"] = quantity
	}
	encoded, err := json.Marshal(lines)
	if err != nil {
		return nil, err
	}
	fields["line_items"] = encoded
	return json.Marshal(fields)
}
