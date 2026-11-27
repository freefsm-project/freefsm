package authorization

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/jackc/pgx/v5"
)

type LeaseExportMode string

const (
	LeaseExportOperational LeaseExportMode = "operational"
	LeaseExportBilling     LeaseExportMode = "billing"
)

type LeaseExportFilter struct {
	AsOf          string `json:"as_of"`
	Search        string `json:"search"`
	Lifecycle     string `json:"lifecycle"`
	Customer      int64  `json:"customer"`
	Asset         int64  `json:"asset"`
	Premises      int64  `json:"premises"`
	EndingFrom    string `json:"ending_from"`
	EndingThrough string `json:"ending_through"`
	Archive       string `json:"archive"`
	Placement     string `json:"placement"`
	Billing       string `json:"billing"`
	Sort          string `json:"sort"`
}

func leaseExportFilter(q LeaseReportQuery) LeaseExportFilter {
	return LeaseExportFilter{AsOf: q.AsOf, Search: q.Search, Lifecycle: q.Lifecycle, Customer: q.Customer, Asset: q.Asset, Premises: q.Premises, EndingFrom: q.EndingFrom, EndingThrough: q.EndingThrough, Archive: q.Archive, Placement: q.Placement, Billing: q.Billing, Sort: q.Sort}
}
func (f LeaseExportFilter) reportQuery() LeaseReportQuery {
	return LeaseReportQuery{AsOf: f.AsOf, Search: f.Search, Lifecycle: f.Lifecycle, Customer: f.Customer, Asset: f.Asset, Premises: f.Premises, EndingFrom: f.EndingFrom, EndingThrough: f.EndingThrough, Archive: f.Archive, Placement: f.Placement, Billing: f.Billing, Sort: f.Sort}
}

func NormalizeLeaseExportFilter(f LeaseExportFilter) (LeaseExportFilter, error) {
	return normalizeLeaseReportSelection(f)
}

func normalizeLeaseReportSelection(f LeaseExportFilter) (LeaseExportFilter, error) {
	if f.Customer < 0 || f.Asset < 0 || f.Premises < 0 || !slices.Contains([]string{"", "draft", "scheduled", "active", "month_to_month", "terminated"}, f.Lifecycle) || !slices.Contains([]string{"", "unplaced", "wrong_premises"}, f.Placement) || !slices.Contains([]string{"", "review", "void", "ungenerated", "pending", "failed"}, f.Billing) || !slices.Contains([]string{"", "all", "archived"}, f.Archive) || !slices.Contains([]string{"", "number", "number_desc"}, f.Sort) {
		return f, ErrConflict
	}
	for _, date := range []string{f.AsOf, f.EndingFrom, f.EndingThrough} {
		if date != "" {
			if _, err := time.Parse(time.DateOnly, date); err != nil {
				return f, ErrConflict
			}
		}
	}
	if f.EndingFrom != "" && f.EndingThrough != "" && f.EndingFrom > f.EndingThrough {
		return f, ErrConflict
	}
	if f.Sort == "" {
		f.Sort = "number"
	}
	return f, nil
}

func AuthorizeLeaseExport(p Principal, mode LeaseExportMode, f LeaseExportFilter) error {
	if mode != LeaseExportOperational && mode != LeaseExportBilling || !p.Allows("lease.export", All) || !p.Allows("lease.view", All) {
		return ErrForbidden
	}
	if mode == LeaseExportOperational && f.Billing != "" || mode == LeaseExportBilling && !p.Allows("lease.billing.view", All) {
		return ErrForbidden
	}
	if f.Customer != 0 && !p.Allows("customer.view", All) || (f.Asset != 0 || f.Placement != "") && !p.Allows("asset.view", All) || f.Premises != 0 && !p.Allows("location.view", All) || f.Archive != "" && !p.Allows("lease.archived.view", All) {
		return ErrForbidden
	}
	if f.Billing != "" && (!p.Allows("invoice.view", Assigned) || !p.Allows("pricing.view", Assigned)) {
		return ErrForbidden
	}
	if slices.Contains([]string{"pending", "failed", "ungenerated"}, f.Billing) && !leaseReportGenerationComplete(p) {
		return ErrForbidden
	}
	_, err := NormalizeLeaseExportFilter(f)
	return err
}

func AuthorizeLeaseExportSelection(ctx context.Context, tx pgx.Tx, p Principal, mode LeaseExportMode, f LeaseExportFilter) error {
	return authorizeLeaseExportInputs(ctx, tx, p, mode, f)
}

func ValidateLeaseExportFreshSelection(ctx context.Context, tx pgx.Tx, p Principal, mode LeaseExportMode, f LeaseExportFilter) error {
	if err := AuthorizeLeaseExport(p, mode, f); err != nil {
		return err
	}
	var today string
	if err := tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text`, p.CompanyID).Scan(&today); err != nil {
		return err
	}
	if f.AsOf > today || (f.Billing == "failed" || f.Billing == "pending") && f.AsOf != "" && f.AsOf < today {
		return ErrConflict
	}
	return authorizeLeaseExportInputs(ctx, tx, p, mode, f)
}

// Rechecking immutable bytes deliberately excludes fresh temporal eligibility.
// A queue selected today remains downloadable tomorrow within its artifact TTL.
func authorizeLeaseExportInputs(ctx context.Context, tx pgx.Tx, p Principal, mode LeaseExportMode, f LeaseExportFilter) error {
	if err := AuthorizeLeaseExport(p, mode, f); err != nil {
		return err
	}
	for _, r := range []struct {
		kind string
		id   int64
	}{{"customer", f.Customer}, {"asset", f.Asset}, {"location", f.Premises}} {
		if r.id != 0 {
			if err := checkLeaseExportRelated(ctx, tx, p, r.kind, LeaseExportRelatedReference{ID: r.id}); err != nil {
				return err
			}
		}
	}
	return nil
}

type LeaseExportMeta struct {
	AsOf, Timezone string
	SnapshotAt     time.Time
}
type LeaseExportSummary struct {
	Meta                  LeaseExportMeta
	LeaseCount, RentCents int64
	RentAvailable         bool
	Reference             LeaseExportReference
}
type LeaseExportEntry struct {
	Lease                LeaseReportRow
	FirstMonthlyOn       string
	RentTerms            []LeaseExportRentTerm
	BillingRevisions     []LeaseExportBillingRevision
	Periods              []LeaseExportPeriod
	GeneratedInvoices    []LeaseExportGeneratedInvoice
	FailureHistory       string
	PositionAvailability string
	Reference            LeaseExportReference
}
type LeaseExportRentTerm struct {
	AssetID, RentCents   int64
	TaxRate, EffectiveOn string
}
type LeaseExportBillingRevision struct {
	ID, ActorID                     int64
	EffectiveOn, RecordedAt, Reason string
	Changes                         []LeaseExportBillingChange
}
type LeaseExportBillingChange struct {
	Ordinal                                  int
	AssetID, BeforeRentCents, AfterRentCents int64
	BeforeTaxRate, AfterTaxRate              string
}
type LeaseExportPeriod struct {
	Kind, DueOn, State, Reason string
	ObligationID               int64
}
type LeaseExportGeneratedInvoice struct {
	Invoice                                      LeaseReportInvoice
	ObligationID, ObligationRevisionID, TaxCents int64
	TaxRate                                      string
	TaxExempt                                    bool
	Lines                                        []LeaseExportInvoiceLine
}
type LeaseExportInvoiceLine struct {
	Ordinal                    int
	AssetID, BillingRevisionID int64
	Amount                     string
	Taxable                    bool
}

type LeaseExportReference struct {
	Selection          LeaseExportSelectionReference `json:"selection"`
	Version            int                           `json:"version"`
	Mode               LeaseExportMode               `json:"mode"`
	Summary            bool                          `json:"summary"`
	Filter             LeaseExportFilter             `json:"filter"`
	CompleteGeneration bool                          `json:"complete_generation"`
	Leases             []LeaseExportLeaseReference   `json:"leases"`
}

// Selected inputs retain their own archive prerequisites even when no Lease
// matched, or when the selected resource is subsequently restored.
type LeaseExportSelectionReference struct {
	Customers []LeaseExportRelatedReference `json:"customers"`
	Premises  []LeaseExportRelatedReference `json:"premises"`
	Assets    []LeaseExportRelatedReference `json:"assets"`
}

func readLeaseExportSelectionReference(ctx context.Context, tx pgx.Tx, p Principal, f LeaseExportFilter) (LeaseExportSelectionReference, error) {
	r := LeaseExportSelectionReference{Customers: []LeaseExportRelatedReference{}, Premises: []LeaseExportRelatedReference{}, Assets: []LeaseExportRelatedReference{}}
	for _, selected := range []struct {
		table string
		id    int64
		refs  *[]LeaseExportRelatedReference
	}{{"customers", f.Customer, &r.Customers}, {"locations", f.Premises, &r.Premises}, {"assets", f.Asset, &r.Assets}} {
		if selected.id == 0 {
			continue
		}
		v := LeaseExportRelatedReference{ID: selected.id}
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM `+selected.table+` WHERE id=$1 AND company_id=$2`, selected.id, p.CompanyID).Scan(&v.Archived); err != nil {
			return r, err
		}
		*selected.refs = append(*selected.refs, v)
	}
	return r, nil
}

type LeaseExportRelatedReference struct {
	ID       int64 `json:"id"`
	Archived bool  `json:"archived"`
}
type LeaseExportInvoiceReference struct {
	ID         int64 `json:"id"`
	JobID      int64 `json:"job_id"`
	Archived   bool  `json:"archived"`
	Settlement bool  `json:"settlement"`
}
type LeaseExportLeaseReference struct {
	ID        int64                         `json:"id"`
	Archived  bool                          `json:"archived"`
	Customers []LeaseExportRelatedReference `json:"customers"`
	Premises  []LeaseExportRelatedReference `json:"premises"`
	Assets    []LeaseExportRelatedReference `json:"assets"`
	Invoices  []LeaseExportInvoiceReference `json:"invoices"`
	Actors    []int64                       `json:"actors"`
}

// One transaction owns selection, child projections, references and summary.
// The caller owns publication, so a failed emit cannot produce a partial artifact.
func LeaseExportSnapshot(ctx context.Context, tx pgx.Tx, p Principal, mode LeaseExportMode, f LeaseExportFilter, emit func(LeaseExportMeta, LeaseExportEntry) error) (LeaseExportSummary, error) {
	var out LeaseExportSummary
	var err error
	f, err = NormalizeLeaseExportFilter(f)
	if err != nil {
		return out, err
	}
	if err = authorizeLeaseExportInputs(ctx, tx, p, mode, f); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text,coalesce((SELECT timezone FROM company_settings WHERE company_id=$1),'UTC'),transaction_timestamp()`, p.CompanyID).Scan(&out.Meta.AsOf, &out.Meta.Timezone, &out.Meta.SnapshotAt); err != nil {
		return out, err
	}
	today := out.Meta.AsOf
	if f.AsOf != "" {
		out.Meta.AsOf = f.AsOf
	}
	out.Reference = LeaseExportReference{Version: 1, Mode: mode, Summary: true, Filter: f, Leases: []LeaseExportLeaseReference{}}
	out.Reference.Selection, err = readLeaseExportSelectionReference(ctx, tx, p, f)
	if err != nil {
		return out, err
	}
	out.RentAvailable = true
	report, err := readLeaseReportAt(ctx, tx, p, f.reportQuery(), mode == LeaseExportBilling, true, today, out.Meta.SnapshotAt.Format(time.RFC3339Nano), func(row LeaseReportRow) error {
		entry := LeaseExportEntry{Lease: row, FirstMonthlyOn: row.FirstMonthlyOn, PositionAvailability: "available"}
		if row.PositionUnavailable {
			entry.PositionAvailability = "unavailable"
			out.RentAvailable = false
		}
		if mode == LeaseExportBilling {
			entry.RentTerms, entry.BillingRevisions, entry.GeneratedInvoices = row.RentTerms, row.BillingRevisions, row.GeneratedInvoices
			entry.FailureHistory = "current"
			if out.Meta.AsOf < today {
				entry.FailureHistory = "unavailable"
			}
			for _, period := range row.Periods {
				entry.Periods = append(entry.Periods, LeaseExportPeriod{Kind: period.Kind, DueOn: period.DueOn, State: period.State, Reason: period.Reason, ObligationID: period.ObligationID})
			}
			if row.GenerationStatusUnavailable {
				entry.Periods = []LeaseExportPeriod{{State: "unavailable", Reason: "Complete generation authority unavailable"}}
			}
		}
		ref, err := leaseExportRowReference(ctx, tx, p, entry)
		if err != nil {
			return err
		}
		complete := mode == LeaseExportBilling && !row.GenerationStatusUnavailable && row.Lifecycle != "draft"
		// Explicit negative selection has a prerequisite even for zero matches.
		complete = complete || slices.Contains([]string{"pending", "failed", "ungenerated"}, f.Billing)
		entry.Reference = LeaseExportReference{Version: 1, Mode: mode, Filter: f, CompleteGeneration: complete, Leases: []LeaseExportLeaseReference{ref}}
		entry.Reference.Selection = out.Reference.Selection
		out.Reference.CompleteGeneration = out.Reference.CompleteGeneration || complete
		out.Reference.Leases = append(out.Reference.Leases, ref)
		return emit(out.Meta, entry)
	})
	if err != nil {
		return out, err
	}
	out.Reference.CompleteGeneration = out.Reference.CompleteGeneration || slices.Contains([]string{"pending", "failed", "ungenerated"}, f.Billing)
	slices.SortFunc(out.Reference.Leases, func(a, b LeaseExportLeaseReference) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	out.LeaseCount, out.RentCents = int64(report.Total), report.RentCents
	return out, nil
}

func leaseExportAssetVisible(ctx context.Context, tx pgx.Tx, p Principal, id int64) (bool, error) {
	if !p.Allows("asset.view", All) {
		return false, nil
	}
	var visible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, id, p.CompanyID, p.Allows("asset.archived.view", All)).Scan(&visible)
	return visible, err
}

func readLeaseReportBillingDetails(ctx context.Context, tx pgx.Tx, p Principal, e *LeaseExportEntry, at, today string, terms []DraftLeaseTerms, revisions []LeaseBillingRevisionRecord) error {
	e.FailureHistory = e.Lease.FailureHistory
	if e.FailureHistory == "" {
		e.FailureHistory = "current"
		if at < today {
			e.FailureHistory = "unavailable"
		}
	}
	for _, period := range e.Lease.Periods {
		e.Periods = append(e.Periods, LeaseExportPeriod{Kind: period.Kind, DueOn: period.DueOn, State: period.State, Reason: period.Reason, ObligationID: period.ObligationID})
	}
	if e.Lease.GenerationStatusUnavailable {
		e.Periods = []LeaseExportPeriod{{State: "unavailable", Reason: "Complete generation authority unavailable"}}
	}
	var err error
	slices.SortFunc(terms, func(a, b DraftLeaseTerms) int {
		if a.AssetID < b.AssetID {
			return -1
		}
		if a.AssetID > b.AssetID {
			return 1
		}
		return 0
	})
	if e.Lease.Lifecycle != "draft" && e.Lease.Lifecycle != "terminated" && !e.Lease.PositionUnavailable {
		for _, term := range terms {
			if !term.Selected || term.RentCents == nil {
				continue
			}
			id := term.AssetID
			visible, err := leaseExportAssetVisible(ctx, tx, p, id)
			if err != nil {
				return err
			}
			if !visible {
				id = 0
			}
			e.RentTerms = append(e.RentTerms, LeaseExportRentTerm{AssetID: id, RentCents: *term.RentCents, TaxRate: term.TaxRate, EffectiveOn: at})
		}
	}
	var cutoff time.Time
	if err = tx.QueryRow(ctx, `SELECT ($1::date+1)::timestamp AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$2),'UTC')`, at, p.CompanyID).Scan(&cutoff); err != nil {
		return err
	}
	for _, r := range revisions {
		var included bool
		if err = tx.QueryRow(ctx, `SELECT $1::timestamptz<$2`, r.RecordedAt, cutoff).Scan(&included); err != nil {
			return err
		}
		if !included {
			continue
		}
		v := LeaseExportBillingRevision{ID: r.ID, ActorID: r.ActorID, EffectiveOn: r.EffectiveOn, RecordedAt: r.RecordedAt, Reason: r.Reason}
		if !p.Allows("directory.view", All) {
			v.ActorID = 0
		}
		for ordinal, after := range r.After {
			change := LeaseExportBillingChange{Ordinal: ordinal + 1, AssetID: after.AssetID, AfterRentCents: after.RentCents, AfterTaxRate: after.TaxRate}
			for _, before := range r.Before {
				if before.AssetID == after.AssetID {
					change.BeforeRentCents, change.BeforeTaxRate = before.RentCents, before.TaxRate
					break
				}
			}
			visible, err := leaseExportAssetVisible(ctx, tx, p, change.AssetID)
			if err != nil {
				return err
			}
			if !visible {
				change.AssetID = 0
			}
			v.Changes = append(v.Changes, change)
		}
		e.BillingRevisions = append(e.BillingRevisions, v)
	}
	for _, invoice := range e.Lease.Invoices {
		v := LeaseExportGeneratedInvoice{Invoice: invoice}
		var financial string
		var lines []byte
		if err = tx.QueryRow(ctx, `SELECT o.id,coalesce(r.id,0),coalesce(r.financial,o.financial),coalesce(r.line_items,o.line_items) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id LEFT JOIN lease_billing_obligation_revisions r ON r.id=g.obligation_revision_id WHERE g.invoice_id=$1`, invoice.ID).Scan(&v.ObligationID, &v.ObligationRevisionID, &financial, &lines); err != nil {
			return err
		}
		snapshot, err := commercial.Read(financial)
		if err != nil {
			return err
		}
		v.TaxCents, v.TaxRate, v.TaxExempt = snapshot.Result.TaxCents, snapshot.Input.TaxRate, snapshot.Input.TaxExempt
		var captured []LeaseBillingPeriodLine
		if err = json.Unmarshal(lines, &captured); err != nil {
			return err
		}
		for i, line := range captured {
			visible, err := leaseExportAssetVisible(ctx, tx, p, line.AssetID)
			if err != nil {
				return err
			}
			id := line.AssetID
			if !visible {
				id = 0
			}
			v.Lines = append(v.Lines, LeaseExportInvoiceLine{Ordinal: i + 1, AssetID: id, BillingRevisionID: line.BillingRevisionID, Amount: line.Amount, Taxable: line.Taxable})
		}
		e.GeneratedInvoices = append(e.GeneratedInvoices, v)
	}
	return nil
}

func checkLeaseExportRelated(ctx context.Context, tx pgx.Tx, p Principal, kind string, r LeaseExportRelatedReference) error {
	if r.ID <= 0 || !p.Allows(kind+".view", All) || r.Archived && !p.Allows(kind+".archived.view", All) {
		return ErrForbidden
	}
	if kind == "location" {
		var visible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM locations WHERE id=$1 AND company_id=$2 AND (deleted_at IS NULL OR $3))`, r.ID, p.CompanyID, p.Allows("location.archived.view", All)).Scan(&visible); err != nil {
			return err
		}
		if !visible {
			return ErrNotFound
		}
		return nil
	}
	_, err := parentAccess(ctx, tx, p, kind, r.ID, "", false)
	return err
}

func leaseExportRowReference(ctx context.Context, tx pgx.Tx, p Principal, e LeaseExportEntry) (LeaseExportLeaseReference, error) {
	r := LeaseExportLeaseReference{ID: e.Lease.ID, Archived: e.Lease.Archived, Customers: []LeaseExportRelatedReference{}, Premises: []LeaseExportRelatedReference{}, Assets: []LeaseExportRelatedReference{}, Invoices: []LeaseExportInvoiceReference{}, Actors: []int64{}}
	add := func(kind string, id int64, refs *[]LeaseExportRelatedReference) error {
		if id == 0 {
			return nil
		}
		for _, existing := range *refs {
			if existing.ID == id {
				return nil
			}
		}
		var archived bool
		table := map[string]string{"customer": "customers", "location": "locations", "asset": "assets"}[kind]
		if err := tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM `+table+` WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&archived); err != nil {
			return err
		}
		*refs = append(*refs, LeaseExportRelatedReference{ID: id, Archived: archived})
		return nil
	}
	if err := add("customer", e.Lease.CustomerID, &r.Customers); err != nil {
		return r, err
	}
	if err := add("location", e.Lease.PremisesID, &r.Premises); err != nil {
		return r, err
	}
	for _, asset := range e.Lease.Assets {
		if err := add("asset", asset.ID, &r.Assets); err != nil {
			return r, err
		}
	}
	for _, term := range e.RentTerms {
		if err := add("asset", term.AssetID, &r.Assets); err != nil {
			return r, err
		}
	}
	addInvoice := func(id int64, settlement bool) error {
		for i := range r.Invoices {
			if r.Invoices[i].ID == id {
				r.Invoices[i].Settlement = r.Invoices[i].Settlement || settlement
				return nil
			}
		}
		v := LeaseExportInvoiceReference{ID: id, Settlement: settlement}
		if err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0),deleted_at IS NOT NULL FROM invoices WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&v.JobID, &v.Archived); err != nil {
			return err
		}
		r.Invoices = append(r.Invoices, v)
		return nil
	}
	for _, invoice := range e.Lease.Invoices {
		if err := addInvoice(invoice.ID, invoice.SettlementVisible); err != nil {
			return r, err
		}
		for _, asset := range invoice.Assets {
			if err := add("asset", asset.ID, &r.Assets); err != nil {
				return r, err
			}
		}
	}
	for _, invoice := range e.GeneratedInvoices {
		for _, line := range invoice.Lines {
			if err := add("asset", line.AssetID, &r.Assets); err != nil {
				return r, err
			}
		}
	}
	for _, revision := range e.BillingRevisions {
		if revision.ActorID != 0 {
			r.Actors = append(r.Actors, revision.ActorID)
		}
		for _, change := range revision.Changes {
			if err := add("asset", change.AssetID, &r.Assets); err != nil {
				return r, err
			}
		}
	}
	for _, adjustment := range e.Lease.Adjustments {
		if err := addInvoice(adjustment.InvoiceID, false); err != nil {
			return r, err
		}
		if err := add("asset", adjustment.AssetID, &r.Assets); err != nil {
			return r, err
		}
		if adjustment.ActorID != 0 {
			r.Actors = append(r.Actors, adjustment.ActorID)
		}
	}
	for _, list := range [][]LeaseExportRelatedReference{r.Customers, r.Premises, r.Assets} {
		slices.SortFunc(list, func(a, b LeaseExportRelatedReference) int {
			if a.ID < b.ID {
				return -1
			}
			if a.ID > b.ID {
				return 1
			}
			return 0
		})
	}
	slices.SortFunc(r.Invoices, func(a, b LeaseExportInvoiceReference) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	slices.Sort(r.Actors)
	r.Actors = slices.Compact(r.Actors)
	return r, nil
}

// Validate every required collection before doing database work. Nil collections
// indicate a missing/null durable field, not an empty authorized population.
func validLeaseExportReference(mode LeaseExportMode, r LeaseExportReference) bool {
	for _, selected := range []struct {
		id   int64
		refs []LeaseExportRelatedReference
	}{{r.Filter.Customer, r.Selection.Customers}, {r.Filter.Premises, r.Selection.Premises}, {r.Filter.Asset, r.Selection.Assets}} {
		if selected.refs == nil || selected.id == 0 && len(selected.refs) != 0 || selected.id != 0 && (len(selected.refs) != 1 || selected.refs[0].ID != selected.id) {
			return false
		}
	}
	if r.Version != 1 || r.Mode != mode || mode != LeaseExportOperational && mode != LeaseExportBilling || r.Leases == nil || !r.Summary && len(r.Leases) != 1 || mode == LeaseExportOperational && r.CompleteGeneration {
		return false
	}
	f, err := NormalizeLeaseExportFilter(r.Filter)
	if err != nil || f != r.Filter {
		return false
	}
	if slices.Contains([]string{"pending", "failed", "ungenerated"}, f.Billing) && !r.CompleteGeneration {
		return false
	}
	var last int64
	for _, lease := range r.Leases {
		if lease.ID <= last || lease.Customers == nil || lease.Premises == nil || lease.Assets == nil || lease.Invoices == nil || lease.Actors == nil {
			return false
		}
		last = lease.ID
		for _, list := range [][]LeaseExportRelatedReference{lease.Customers, lease.Premises, lease.Assets} {
			var prev int64
			for _, v := range list {
				if v.ID <= prev {
					return false
				}
				prev = v.ID
			}
		}
		var prev int64
		for _, v := range lease.Invoices {
			if v.ID <= prev || v.JobID < 0 || mode == LeaseExportOperational {
				return false
			}
			prev = v.ID
		}
		prev = 0
		for _, id := range lease.Actors {
			if id <= prev || mode == LeaseExportOperational {
				return false
			}
			prev = id
		}
	}
	return true
}

func CheckLeaseExportReference(ctx context.Context, tx pgx.Tx, p Principal, mode LeaseExportMode, r LeaseExportReference) error {
	if !validLeaseExportReference(mode, r) {
		return ErrForbidden
	}
	if err := authorizeLeaseExportInputs(ctx, tx, p, mode, r.Filter); err != nil {
		return err
	}
	for _, selected := range []struct {
		kind string
		refs []LeaseExportRelatedReference
	}{{"customer", r.Selection.Customers}, {"location", r.Selection.Premises}, {"asset", r.Selection.Assets}} {
		for _, ref := range selected.refs {
			if err := checkLeaseExportRelated(ctx, tx, p, selected.kind, ref); err != nil {
				return err
			}
		}
	}
	if r.CompleteGeneration && !leaseReportGenerationComplete(p) {
		return ErrForbidden
	}
	for _, lease := range r.Leases {
		if lease.Archived && !p.Allows("lease.archived.view", All) {
			return ErrForbidden
		}
		if _, err := parentAccess(ctx, tx, p, "lease", lease.ID, "", false); err != nil {
			return err
		}
		for _, group := range []struct {
			kind string
			refs []LeaseExportRelatedReference
		}{{"customer", lease.Customers}, {"location", lease.Premises}, {"asset", lease.Assets}} {
			for _, ref := range group.refs {
				if err := checkLeaseExportRelated(ctx, tx, p, group.kind, ref); err != nil {
					return err
				}
			}
		}
		for _, ref := range lease.Invoices {
			if ref.Archived && !p.Allows("invoice.archived.view", All) {
				return ErrForbidden
			}
			if _, err := parentAccess(ctx, tx, p, "invoice", ref.ID, "", false); err != nil {
				return err
			}
			permissions := []string{"invoice.view", "pricing.view"}
			if ref.Settlement {
				permissions = append(permissions, "settlement.view")
			}
			for _, permission := range permissions {
				if err := parentFacet(ctx, tx, p, "invoice", ref.ID, permission, false); err != nil {
					return err
				}
				if p.Allows(permission, All) {
					continue
				}
				if ref.JobID <= 0 || !p.Allows(permission, Assigned) {
					return ErrForbidden
				}
				allowed, err := assignedJobAccess(ctx, tx, p.UserID, ref.JobID, false)
				if err != nil {
					return err
				}
				if !allowed {
					return ErrForbidden
				}
			}
		}
		for _, id := range lease.Actors {
			if !p.Allows("directory.view", All) {
				return ErrForbidden
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND company_id=$2)`, id, p.CompanyID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
		}
	}
	return nil
}
