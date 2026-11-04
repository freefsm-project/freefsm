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
}
type LeaseReport struct {
	Errors, Inputs                                                    map[string]string
	HasLeases, CanCreate                                              bool
	Query                                                             LeaseReportQuery
	UpdatedAt, AsOf                                                   string
	BillingVisible                                                    bool
	CanCustomer, CanAsset, CanPremises, CanArchive, CanInvoiceFilters bool
	Total                                                             int
	RentCents                                                         int64
	Rows                                                              []LeaseReportRow
}

func (s *Service) LeaseReport(ctx context.Context, a Actor, q LeaseReportQuery) (LeaseReport, error) {
	out := LeaseReport{Query: q}
	if a.Channel == "api" {
		return LeaseReport{}, ErrNotFound
	}
	err := s.WithinSnapshotPermission(ctx, a, "lease.view", All, func(tx pgx.Tx, p Principal) error {
		if err := tx.QueryRow(ctx, `SELECT lease_dependency_today($1)::text,clock_timestamp()::text`, p.CompanyID).Scan(&out.AsOf, &out.UpdatedAt); err != nil {
			return err
		}
		if q.AsOf != "" {
			if _, err := time.Parse(time.DateOnly, q.AsOf); err != nil || q.AsOf > out.AsOf {
				return ErrConflict
			}
			out.AsOf = q.AsOf
		}
		out.BillingVisible = p.Allows("lease.billing.view", All)
		out.CanCustomer = p.Allows("customer.view", All)
		out.CanAsset = p.Allows("asset.view", All)
		out.CanPremises = p.Allows("location.view", All)
		out.CanArchive = p.Allows("lease.archived.view", All)
		out.CanCreate = p.Allows("lease.manage", All) && out.CanCustomer
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leases WHERE company_id=$1 AND (deleted_at IS NULL OR $2))`, p.CompanyID, out.CanArchive).Scan(&out.HasLeases); err != nil {
			return err
		}
		out.CanInvoiceFilters = out.BillingVisible && p.Allows("invoice.view", Assigned) && p.Allows("pricing.view", Assigned)
		if q.Billing != "" && !out.CanInvoiceFilters {
			return ErrForbidden
		}
		if q.Customer != 0 && !p.Allows("customer.view", All) || q.Asset != 0 && !p.Allows("asset.view", All) || q.Premises != 0 && !p.Allows("location.view", All) || q.Placement != "" && !p.Allows("asset.view", All) {
			return ErrForbidden
		}
		if q.Customer < 0 || q.Asset < 0 || q.Premises < 0 || q.Page > 1000000 || !slices.Contains([]string{"", "draft", "scheduled", "active", "month_to_month", "terminated"}, q.Lifecycle) || !slices.Contains([]string{"", "unplaced", "wrong_premises"}, q.Placement) || !slices.Contains([]string{"", "review", "void", "ungenerated"}, q.Billing) || !slices.Contains([]string{"", "all", "archived"}, q.Archive) || !slices.Contains([]string{"", "number", "number_desc"}, q.Sort) {
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
			row, err := readLeaseReportRow(ctx, tx, p, id, out.AsOf, out.BillingVisible)
			if err != nil {
				return err
			}
			if q.Lifecycle != "" && row.Lifecycle != q.Lifecycle || q.Customer != 0 && row.CustomerID != q.Customer || q.Premises != 0 && row.PremisesID != q.Premises {
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
					matched = matched || q.Billing == "ungenerated" && period.State == "ungenerated"
				}
				for _, invoice := range row.Invoices {
					matched = matched || q.Billing == "review" && invoice.Review || q.Billing == "void" && invoice.ManualStatus == "invoice:void"
				}
				if !matched {
					continue
				}
			}
			out.Total++
			out.RentCents += row.RentCents
			if out.Total > (q.Page-1)*q.PerPage && len(out.Rows) < q.PerPage {
				out.Rows = append(out.Rows, row)
			}
		}
		return nil
	})
	if err != nil {
		return LeaseReport{}, err
	}
	return out, nil
}

func readLeaseReportRow(ctx context.Context, tx pgx.Tx, p Principal, id int64, at string, billing bool) (LeaseReportRow, error) {
	var out LeaseReportRow
	var state string
	var customer, premises int64
	err := tx.QueryRow(ctx, `SELECT l.id,l.number_label,l.title,l.lifecycle,coalesce(l.starts_on::text,''),coalesce(l.ends_on::text,''),l.customer_id,coalesce(l.premises_id,0),l.deleted_at IS NOT NULL FROM leases l WHERE l.id=$1`, id).Scan(&out.ID, &out.Number, &out.Title, &state, &out.StartsOn, &out.EndsOn, &customer, &premises, &out.Archived)
	if err != nil {
		return out, err
	}
	if state == "draft" {
		var historical bool
		if err = tx.QueryRow(ctx, `SELECT $1::date<lease_dependency_today($2)`, at, p.CompanyID).Scan(&historical); err != nil {
			return out, err
		}
		if historical {
			var raw []byte
			err = tx.QueryRow(ctx, `SELECT detail->'after' FROM authorization_audit WHERE target_type='lease' AND target_id=$1 AND action='lease.details.change' AND recorded_at<($2::date+1)::timestamp AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$3),'UTC') ORDER BY id DESC LIMIT 1`, id, at, p.CompanyID).Scan(&raw)
			if err != nil && err != pgx.ErrNoRows {
				return out, err
			}
			out.Title, out.StartsOn, out.EndsOn, premises = "Draft details unavailable for selected date", "", "", 0
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
	var noticeAction, noticeCategory, noticeOn, ending string
	err = tx.QueryRow(ctx, `SELECT action,category,notice_on::text,effective_on::text FROM lease_notice_current WHERE lease_id=$1`, id).Scan(&noticeAction, &noticeCategory, &noticeOn, &ending)
	if err != nil && err != pgx.ErrNoRows {
		return out, err
	}
	if err == nil && noticeAction != "withdraw" && noticeOn <= at {
		out.EndingOn = ending
		if ending < at || noticeCategory == "cancel_before_start" {
			state = "terminated"
		}
	}
	day, _ := time.Parse(time.DateOnly, at)
	out.LeaseCalendar, err = leaseCalendar(state, out.StartsOn, out.EndsOn, day)
	if err != nil {
		return out, err
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
	if p.Allows("asset.view", All) {
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
		for _, term := range terms {
			if term.Selected && term.RentCents != nil && out.Lifecycle != "terminated" {
				if out.Lifecycle == "scheduled" {
					out.ScheduledRentCents += *term.RentCents
				} else {
					out.RentCents += *term.RentCents
				}
			}
		}
	}
	if billing {
		if err = readLeaseReportBilling(ctx, tx, p, &out, at); err != nil {
			return out, err
		}
	}
	return out, nil
}
