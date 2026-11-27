package authorization

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LeaseReportQuery selects an effective company-date position, using facts as
// corrected today. It is not a reconstruction of what an operator knew then.
type LeaseReportQuery struct {
	AsOf, Lifecycle, Search, Placement, Billing, Archive, Sort string
	Customer, Asset, Premises                                  int64
	Page, PerPage                                              int
	EndingFrom, EndingThrough                                  string
}

type LeaseReportAsset struct {
	ID                                        int64
	Number, Name, StartsOn, EndsOn, Placement string
}
type LeaseReportRent struct {
	EffectiveOn string
	RentCents   int64
}
type LeaseReportRow struct {
	LeaseCalendar
	ID, CustomerID, PremisesID                                    int64
	Number, Title, StartsOn, EndsOn, EndingOn, Customer, Premises string
	Archived                                                      bool
	Assets                                                        []LeaseReportAsset
	RentCents                                                     int64
	ScheduledRentCents                                            int64
	Invoices                                                      []LeaseReportInvoice
	Periods                                                       []LeaseReportPeriod
	Adjustments                                                   []LeaseAdjustmentRecord
	GenerationStatusUnavailable                                   bool
	RentSchedule                                                  []LeaseReportRent
	PositionUnavailable                                           bool
	FirstMonthlyOn                                                string
	FailureHistory                                                string
	RentTerms                                                     []LeaseExportRentTerm
	BillingRevisions                                              []LeaseExportBillingRevision
	GeneratedInvoices                                             []LeaseExportGeneratedInvoice
}
type LeaseReport struct {
	Errors, Inputs                                                    map[string]string
	HasLeases, CanCreate                                              bool
	Query                                                             LeaseReportQuery
	UpdatedAt, AsOf                                                   string
	BillingVisible                                                    bool
	CanUngeneratedFilter                                              bool
	CanCustomer, CanAsset, CanPremises, CanArchive, CanInvoiceFilters bool
	Total                                                             int
	RentCents                                                         int64
	RentAvailable                                                     bool
	Rows                                                              []LeaseReportRow
}

func (s *Service) LeaseReport(ctx context.Context, a Actor, q LeaseReportQuery) (LeaseReport, error) {
	out := LeaseReport{Query: q}
	if a.Channel == "api" {
		return LeaseReport{}, ErrNotFound
	}
	err := s.WithinSnapshotPermission(ctx, a, "lease.view", All, func(tx pgx.Tx, p Principal) error {
		var err error
		out, err = readLeaseReport(ctx, tx, p, q, p.Allows("lease.billing.view", All), false)
		return err
	})
	return out, err
}

// readLeaseReport uses only its caller's fenced snapshot. allMatching bypasses
// presentation pagination, not selection or authorization.
func readLeaseReport(ctx context.Context, tx pgx.Tx, p Principal, q LeaseReportQuery, billing, allMatching bool) (LeaseReport, error) {
	var today, stamp string
	if err := tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text,transaction_timestamp()::text`, p.CompanyID).Scan(&today, &stamp); err != nil {
		return LeaseReport{}, err
	}
	return readLeaseReportAt(ctx, tx, p, q, billing, allMatching, today, stamp, nil)
}

func readLeaseReportAt(ctx context.Context, tx pgx.Tx, p Principal, q LeaseReportQuery, billing, allMatching bool, today, stamp string, emit func(LeaseReportRow) error) (LeaseReport, error) {
	out := LeaseReport{Query: q, RentAvailable: true}
	out.AsOf, out.UpdatedAt = today, stamp
	err := func() error {
		if _, err := normalizeLeaseReportSelection(leaseExportFilter(q)); err != nil {
			return err
		}
		if q.AsOf != "" {
			if q.AsOf > out.AsOf {
				return ErrConflict
			}
			out.AsOf = q.AsOf
		}
		out.BillingVisible = billing && p.Allows("lease.billing.view", All)
		out.CanCustomer = p.Allows("customer.view", All)
		out.CanAsset = p.Allows("asset.view", All)
		out.CanPremises = p.Allows("location.view", All)
		out.CanArchive = p.Allows("lease.archived.view", All)
		out.CanCreate = p.Allows("lease.manage", All) && out.CanCustomer
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leases WHERE company_id=$1 AND (deleted_at IS NULL OR $2))`, p.CompanyID, out.CanArchive).Scan(&out.HasLeases); err != nil {
			return err
		}
		out.CanInvoiceFilters = out.BillingVisible && p.Allows("invoice.view", Assigned) && p.Allows("pricing.view", Assigned)
		out.CanUngeneratedFilter = out.BillingVisible && leaseReportGenerationComplete(p)
		if q.Billing != "" && !out.CanInvoiceFilters {
			return ErrForbidden
		}
		if slices.Contains([]string{"ungenerated", "pending", "failed"}, q.Billing) && !out.CanUngeneratedFilter {
			if q.Billing == "ungenerated" {
				return ErrConflict
			}
			return ErrForbidden
		}
		if q.Billing == "failed" || q.Billing == "pending" {
			if out.AsOf < today {
				return ErrConflict
			}
		}
		if q.Customer != 0 && !p.Allows("customer.view", All) || q.Asset != 0 && !p.Allows("asset.view", All) || q.Premises != 0 && !p.Allows("location.view", All) || q.Placement != "" && !p.Allows("asset.view", All) {
			return ErrForbidden
		}
		if q.Page > 1000000 {
			return ErrConflict
		}
		if q.Archive != "" && !p.Allows("lease.archived.view", All) {
			return ErrForbidden
		}
		if q.Page < 1 {
			q.Page = 1
		}
		if q.PerPage != 25 && q.PerPage != 50 && q.PerPage != 100 {
			q.PerPage = 25
		}
		out.Query = q
		// Read the candidate identity list once. Related authorization is applied
		// before filtering, totals, and paging, inside this same fenced snapshot.
		rows, err := tx.Query(ctx, `SELECT id FROM leases WHERE company_id=$1 AND (deleted_at IS NULL OR $2) AND ($3<>'archived' OR deleted_at IS NOT NULL)
 AND (lifecycle<>'draft' OR created_at<($4::date+1)::timestamp AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$1),'UTC'))
 ORDER BY document_number,id`, p.CompanyID, q.Archive != "", q.Archive, out.AsOf)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if q.Sort == "number_desc" {
			slices.Reverse(ids)
		}
		for _, id := range ids {
			row, err := readLeaseReportRow(ctx, tx, p, id, out.AsOf, today, out.BillingVisible)
			if err != nil {
				return err
			}
			if row.PositionUnavailable && (q.Lifecycle != "" || q.EndingFrom != "" || q.EndingThrough != "" || q.Asset != 0 || q.Placement != "" || q.Billing != "") {
				return ErrConflict
			}
			if q.Lifecycle != "" && row.Lifecycle != q.Lifecycle || q.Customer != 0 && row.CustomerID != q.Customer || q.Premises != 0 && row.PremisesID != q.Premises {
				continue
			}
			ending := row.EndingOn
			if ending == "" {
				ending = row.EndsOn
			}
			if q.EndingFrom != "" && (ending == "" || ending < q.EndingFrom) || q.EndingThrough != "" && (ending == "" || ending > q.EndingThrough) {
				continue
			}
			matchedAsset, matchedPlacement := q.Asset == 0, q.Placement == ""
			search := row.Number + " " + row.Title + " " + row.Customer + " " + row.Premises
			for _, asset := range row.Assets {
				matchedAsset = matchedAsset || asset.ID == q.Asset
				matchedPlacement = matchedPlacement || asset.Placement == q.Placement
				search += " " + asset.Number
			}
			if !matchedAsset || !matchedPlacement || !strings.Contains(strings.ToLower(search), strings.ToLower(q.Search)) {
				continue
			}
			if q.Billing != "" {
				matched := false
				for _, period := range row.Periods {
					owed := slices.Contains([]string{"pending", "failed", "inactive", "ungenerated"}, period.State)
					matched = matched || q.Billing == "ungenerated" && owed && period.DueOn <= out.AsOf || q.Billing == "pending" && (period.State == "pending" || period.State == "failed") || q.Billing == "failed" && period.State == "failed"
				}
				for _, invoice := range row.Invoices {
					matched = matched || q.Billing == "review" && invoice.Review || q.Billing == "void" && invoice.ManualStatus == "invoice:void"
				}
				if !matched {
					continue
				}
			}
			out.Total++
			if row.PositionUnavailable {
				out.RentAvailable = false
			}
			out.RentCents += row.RentCents
			if emit != nil {
				if err := emit(row); err != nil {
					return err
				}
				continue
			}
			if allMatching || out.Total > (q.Page-1)*q.PerPage && len(out.Rows) < q.PerPage {
				out.Rows = append(out.Rows, row)
			}
		}
		return nil
	}()
	if err != nil {
		return LeaseReport{}, err
	}
	return out, nil
}

func readLeaseReportRow(ctx context.Context, tx pgx.Tx, p Principal, id int64, at, today string, billing bool) (LeaseReportRow, error) {
	var out LeaseReportRow
	out.GenerationStatusUnavailable = billing
	var state string
	var customer, premises int64
	err := tx.QueryRow(ctx, `SELECT l.id,l.number_label,l.title,l.lifecycle,coalesce(l.starts_on::text,''),coalesce(l.ends_on::text,''),l.customer_id,coalesce(l.premises_id,0),l.deleted_at IS NOT NULL FROM leases l WHERE l.id=$1`, id).Scan(&out.ID, &out.Number, &out.Title, &state, &out.StartsOn, &out.EndsOn, &customer, &premises, &out.Archived)
	if err != nil {
		return out, err
	}
	if state == "draft" {
		if at < today {
			var raw []byte
			err = tx.QueryRow(ctx, `SELECT detail->'after' FROM authorization_audit WHERE target_type='lease' AND target_id=$1 AND action='lease.details.change' AND recorded_at<($2::date+1)::timestamp AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$3),'UTC') ORDER BY id DESC LIMIT 1`, id, at, p.CompanyID).Scan(&raw)
			if err != nil && err != pgx.ErrNoRows {
				return out, err
			}
			out.Title, out.StartsOn, out.EndsOn, premises = "Draft details unavailable for selected date", "", "", 0
			out.PositionUnavailable = err == pgx.ErrNoRows
			if err == nil {
				var snapshot DraftLeaseBase
				if err = json.Unmarshal(raw, &snapshot); err != nil {
					return out, err
				}
				out.Title, out.StartsOn, out.EndsOn = snapshot.Title, snapshot.StartsOn, snapshot.EndsOn
				if snapshot.PremisesID != nil {
					premises = *snapshot.PremisesID
				}
			}
		}
	}
	if state == "draft" {
		out.Lifecycle = "draft"
	} else {
		position, err := readLeaseAgreementPosition(ctx, tx, id, at)
		if err != nil {
			return out, err
		}
		out.LeaseCalendar = position.LeaseCalendar
		out.StartsOn, out.EndsOn, out.EndingOn, out.FirstMonthlyOn = position.StartsOn, position.EndsOn, position.EndingOn, position.FirstMonthlyOn
		out.PositionUnavailable = position.Unavailable
	}
	if p.Allows("customer.view", All) {
		err = tx.QueryRow(ctx, `SELECT id,display_name FROM customers WHERE id=$1 AND (deleted_at IS NULL OR $2)`, customer, p.Allows("customer.archived.view", All)).Scan(&out.CustomerID, &out.Customer)
		if err != nil && err != pgx.ErrNoRows {
			return out, err
		}
	}
	if premises != 0 && p.Allows("location.view", All) {
		err = tx.QueryRow(ctx, `SELECT id,title FROM locations WHERE id=$1 AND (deleted_at IS NULL OR $2)`, premises, p.Allows("location.archived.view", All)).Scan(&out.PremisesID, &out.Premises)
		if err != nil && err != pgx.ErrNoRows {
			return out, err
		}
	}
	if state == "draft" {
		return out, nil
	}
	// Coverage dependencies are the inclusive effective projection of immutable
	// commencement, coverage revisions and factual corrections, not current Assets.
	if p.Allows("asset.view", All) && !out.PositionUnavailable && out.Lifecycle != "terminated" {
		rows, err := tx.Query(ctx, `SELECT a.id,a.number,a.name,d.starts_on::text,coalesce(d.ends_on::text,''),CASE WHEN ap.asset_id IS NULL THEN 'unknown' WHEN ap.customer_id IS NULL THEN 'unplaced' WHEN ap.customer_id=$3 AND ap.location_id IS DISTINCT FROM NULLIF($4,0) THEN 'wrong_premises' WHEN ap.customer_id=$3 THEN 'placed' ELSE 'other placement' END FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id LEFT JOIN asset_association_periods ap ON ap.asset_id=a.id AND (ap.starts_on IS NULL OR ap.starts_on<=$2::date) AND (ap.ends_before IS NULL OR ap.ends_before>$2::date) WHERE d.lease_id=$1 AND d.starts_on<=$2::date AND (d.ends_on IS NULL OR d.ends_on>=$2::date) AND (a.deleted_at IS NULL OR $5) ORDER BY a.number,a.id`, id, at, customer, premises, p.Allows("asset.archived.view", All))
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var asset LeaseReportAsset
			if err = rows.Scan(&asset.ID, &asset.Number, &asset.Name, &asset.StartsOn, &asset.EndsOn, &asset.Placement); err != nil {
				rows.Close()
				return out, err
			}
			out.Assets = append(out.Assets, asset)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	var selectedTerms []DraftLeaseTerms
	var reportRevisions []LeaseBillingRevisionRecord
	if billing {
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT terms FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&raw)
		if err == pgx.ErrNoRows {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		var terms []DraftLeaseTerms
		if err = json.Unmarshal(raw, &terms); err != nil {
			return out, err
		}
		records, err := readCoverageEvents(ctx, tx, id, false)
		if err != nil {
			return out, err
		}
		revisions, err := readLeaseBillingRevisions(ctx, tx, id)
		if err != nil {
			return out, err
		}
		// Approved future changes are a separately labelled schedule, never part
		// of the selected-date amount or any matching-set total.
		dates := []string{out.StartsOn}
		for _, revision := range revisions {
			dates = append(dates, revision.EffectiveOn)
		}
		for _, record := range records {
			if record.Action != "withdraw" {
				day, err := time.Parse(time.DateOnly, record.EffectiveOn)
				if err != nil {
					return out, err
				}
				dates = append(dates, time.Date(day.Year(), day.Month()+1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly))
			}
		}
		slices.Sort(dates)
		for _, effective := range slices.Compact(dates) {
			if effective < out.StartsOn || out.EndingOn != "" && effective > out.EndingOn {
				continue
			}
			selected, err := replayLeaseBillingTerms(terms, records, effective, "monthly", revisions)
			if err != nil {
				return out, err
			}
			entry := LeaseReportRent{EffectiveOn: effective}
			for _, term := range selected {
				if term.Selected && term.RentCents != nil {
					entry.RentCents += *term.RentCents
				}
			}
			out.RentSchedule = append(out.RentSchedule, entry)
		}
		terms, err = replayLeaseBillingTerms(terms, records, at, "monthly", revisions)
		if err != nil {
			return out, err
		}
		selectedTerms, reportRevisions = terms, revisions
		for _, term := range terms {
			if term.Selected && term.RentCents != nil && out.Lifecycle != "terminated" && !out.PositionUnavailable {
				if out.Lifecycle == "scheduled" {
					out.ScheduledRentCents += *term.RentCents
				} else {
					out.RentCents += *term.RentCents
				}
			}
		}
	}
	if billing {
		if err = readLeaseReportBilling(ctx, tx, p, &out, at, today); err != nil {
			return out, err
		}
		entry := LeaseExportEntry{Lease: out}
		if err = readLeaseReportBillingDetails(ctx, tx, p, &entry, at, today, selectedTerms, reportRevisions); err != nil {
			return out, err
		}
		out.RentTerms, out.BillingRevisions, out.GeneratedInvoices = entry.RentTerms, entry.BillingRevisions, entry.GeneratedInvoices
	}
	return out, nil
}
