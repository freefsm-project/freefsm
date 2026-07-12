package objectref

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/asset"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/ent/estimate"
	"github.com/freefsm-project/freefsm/internal/ent/invoice"
	"github.com/freefsm-project/freefsm/internal/ent/item"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/project"
)

type EntDirectory struct {
	client *ent.Client
}

func NewEntDirectory(client *ent.Client) *EntDirectory {
	return &EntDirectory{client: client}
}

func (d *EntDirectory) Parse(objectType string, objectID int64) (Ref, error) {
	return Parse(objectType, objectID)
}

func (d *EntDirectory) Describe(t Type) (Descriptor, bool) {
	return Describe(t)
}

func (d *EntDirectory) Supports(t Type, cap Capability) bool {
	return t.Has(cap)
}

func (d *EntDirectory) Exists(ctx context.Context, ref Ref, mode ExistenceMode) (bool, error) {
	if err := validateExists(ref, mode); err != nil {
		return false, err
	}

	switch ref.Type {
	case TypeCustomer:
		q := d.client.Customer.Query().Where(customer.IDEQ(ref.ID))
		if mode == ExistsActive {
			q = q.Where(customer.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	case TypeJob:
		q := d.client.Job.Query().Where(job.IDEQ(ref.ID))
		if mode == ExistsActive {
			q = q.Where(job.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	case TypeProject:
		q := d.client.Project.Query().Where(project.IDEQ(ref.ID))
		if mode == ExistsActive {
			q = q.Where(project.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	case TypeEstimate:
		q := d.client.Estimate.Query().Where(estimate.IDEQ(ref.ID), estimate.ConversionHiddenAtIsNil())
		if mode == ExistsActive {
			q = q.Where(estimate.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	case TypeInvoice:
		q := d.client.Invoice.Query().Where(invoice.IDEQ(ref.ID), invoice.ConversionHiddenAtIsNil())
		if mode == ExistsActive {
			q = q.Where(invoice.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	case TypeAsset:
		q := d.client.Asset.Query().Where(asset.IDEQ(ref.ID))
		if mode == ExistsActive {
			q = q.Where(asset.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	case TypeItem:
		q := d.client.Item.Query().Where(item.IDEQ(ref.ID))
		if mode == ExistsActive {
			q = q.Where(item.DeletedAtIsNil())
		}
		return q.Exist(ctx)
	default:
		return d.existsAny(ctx, ref)
	}
}

func (d *EntDirectory) TagTargetCompanyID(ctx context.Context, ref Ref) (int64, error) {
	if !ref.Valid() {
		return 0, fmt.Errorf("company ownership: invalid object reference")
	}
	var companyID *int64
	var err error
	switch ref.Type {
	case TypeCustomer:
		var v *ent.Customer
		v, err = d.client.Customer.Get(ctx, ref.ID)
		if err == nil {
			companyID = v.CompanyID
		}
	case TypeJob:
		var v *ent.Job
		v, err = d.client.Job.Get(ctx, ref.ID)
		if err == nil {
			companyID = v.CompanyID
		}
	case TypeProject:
		var v *ent.Project
		v, err = d.client.Project.Get(ctx, ref.ID)
		if err == nil {
			companyID = v.CompanyID
		}
	case TypeEstimate:
		var v *ent.Estimate
		v, err = d.client.Estimate.Get(ctx, ref.ID)
		if err == nil {
			companyID = v.CompanyID
		}
	case TypeInvoice:
		var v *ent.Invoice
		v, err = d.client.Invoice.Get(ctx, ref.ID)
		if err == nil {
			companyID = v.CompanyID
		}
	case TypeAsset:
		var v *ent.Asset
		v, err = d.client.Asset.Get(ctx, ref.ID)
		if err == nil {
			companyID = v.CompanyID
		}
	default:
		return 0, fmt.Errorf("company ownership unsupported for %s", ref.Type)
	}
	if err != nil {
		return 0, err
	}
	if companyID == nil || *companyID <= 0 {
		return 0, fmt.Errorf("%w: %s %d", ErrOwnershipMissing, ref.Type, ref.ID)
	}
	return *companyID, nil
}

func (d *EntDirectory) DisplayName(ctx context.Context, ref Ref) (string, error) {
	if !Known(ref.Type) {
		return "", fmt.Errorf("%w: %s", ErrUnknownType, ref.Type)
	}
	if ref.ID <= 0 {
		return "", fmt.Errorf("%w: %d", ErrInvalidID, ref.ID)
	}
	return d.lookupName(ctx, ref), nil
}

func (d *EntDirectory) URL(ref Ref) (string, bool) {
	return URL(ref)
}

func (d *EntDirectory) existsAny(ctx context.Context, ref Ref) (bool, error) {
	switch ref.Type {
	case TypeTimeEntry:
		_, err := d.client.TimeEntry.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeAssetType:
		_, err := d.client.AssetType.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeAssetStatus:
		_, err := d.client.AssetStatus.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeCompanySettings:
		_, err := d.client.CompanySettings.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeCustomField:
		_, err := d.client.CustomFieldDefinition.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeJobStatus:
		_, err := d.client.Status.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeTag:
		_, err := d.client.Tag.Get(ctx, ref.ID)
		return existsFromGet(err)
	case TypeUser:
		_, err := d.client.User.Get(ctx, ref.ID)
		return existsFromGet(err)
	default:
		return false, fmt.Errorf("%w: %s", ErrUnknownType, ref.Type)
	}
}

func existsFromGet(err error) (bool, error) {
	if err == nil {
		return true, nil
	}
	if ent.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (d *EntDirectory) lookupName(ctx context.Context, ref Ref) string {
	switch ref.Type {
	case TypeCustomer:
		c, err := d.client.Customer.Get(ctx, ref.ID)
		if err == nil {
			return c.DisplayName
		}
	case TypeJob:
		j, err := d.client.Job.Get(ctx, ref.ID)
		if err == nil {
			return j.JobType
		}
	case TypeProject:
		p, err := d.client.Project.Get(ctx, ref.ID)
		if err == nil {
			return p.Name
		}
	case TypeEstimate:
		e, err := d.client.Estimate.Query().Where(estimate.IDEQ(ref.ID), estimate.ConversionHiddenAtIsNil()).Only(ctx)
		if err == nil {
			return e.Title
		}
	case TypeInvoice:
		i, err := d.client.Invoice.Query().Where(invoice.IDEQ(ref.ID), invoice.ConversionHiddenAtIsNil()).Only(ctx)
		if err == nil {
			return i.Title
		}
	case TypeAsset:
		a, err := d.client.Asset.Get(ctx, ref.ID)
		if err == nil {
			return a.Name
		}
	case TypeItem:
		i, err := d.client.Item.Get(ctx, ref.ID)
		if err == nil {
			return i.Name
		}
	case TypeTimeEntry:
		te, err := d.client.TimeEntry.Get(ctx, ref.ID)
		if err == nil {
			cs, _ := d.client.CompanySettings.Query().First(ctx)
			loc := companySettingsLocation(cs)
			clockIn := formatCompanyDateTime(te.ClockIn, loc, cs)
			if te.ClockOut != nil {
				return fmt.Sprintf("%s — %s", clockIn, formatCompanyTime(*te.ClockOut, loc, cs))
			}
			return clockIn
		}
	case TypeAssetType:
		at, err := d.client.AssetType.Get(ctx, ref.ID)
		if err == nil {
			return at.Name
		}
	case TypeAssetStatus:
		as, err := d.client.AssetStatus.Get(ctx, ref.ID)
		if err == nil {
			return as.Name
		}
	case TypeJobStatus:
		st, err := d.client.Status.Get(ctx, ref.ID)
		if err == nil {
			return st.Name
		}
	case TypeCompanySettings:
		cs, err := d.client.CompanySettings.Get(ctx, ref.ID)
		if err == nil {
			if cs.BusinessName != "" {
				return cs.BusinessName
			}
			return "Company Settings"
		}
	case TypeCustomField:
		cf, err := d.client.CustomFieldDefinition.Get(ctx, ref.ID)
		if err == nil {
			return cf.Name
		}
	case TypeTag:
		t, err := d.client.Tag.Get(ctx, ref.ID)
		if err == nil {
			return t.Name
		}
	case TypeUser:
		u, err := d.client.User.Get(ctx, ref.ID)
		if err == nil {
			return u.Name
		}
	}
	return fallbackName(ref)
}

const defaultDateFormat = "Jan 2, 2006"

func formatCompanyDateTime(t time.Time, loc *time.Location, cs *ent.CompanySettings) string {
	if t.IsZero() {
		return ""
	}
	dateLayout := normalizeDateFormat(companyDateFormat(cs))
	return t.In(loc).Format(dateLayout + " " + timeLayoutForDateFormat(dateLayout))
}

func formatCompanyTime(t time.Time, loc *time.Location, cs *ent.CompanySettings) string {
	if t.IsZero() {
		return ""
	}
	dateLayout := normalizeDateFormat(companyDateFormat(cs))
	return t.In(loc).Format(timeLayoutForDateFormat(dateLayout))
}

func companySettingsLocation(cs *ent.CompanySettings) *time.Location {
	if cs != nil && cs.Timezone != "" {
		if loc, err := time.LoadLocation(cs.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

func companyDateFormat(cs *ent.CompanySettings) string {
	if cs == nil {
		return defaultDateFormat
	}
	return cs.DateFormat
}

func normalizeDateFormat(value string) string {
	value = strings.TrimSpace(value)
	for _, option := range dateFormatOptions() {
		if value == option.dateLayout {
			return value
		}
	}
	return defaultDateFormat
}

func timeLayoutForDateFormat(dateLayout string) string {
	for _, option := range dateFormatOptions() {
		if option.dateLayout == dateLayout {
			return option.timeLayout
		}
	}
	return "3:04 PM"
}

type dateFormatOption struct {
	dateLayout string
	timeLayout string
}

func dateFormatOptions() []dateFormatOption {
	return []dateFormatOption{
		{dateLayout: "Jan 2, 2006", timeLayout: "3:04 PM"},
		{dateLayout: "January 2, 2006", timeLayout: "3:04 PM"},
		{dateLayout: "01/02/2006", timeLayout: "3:04 PM"},
		{dateLayout: "02/01/2006", timeLayout: "15:04"},
		{dateLayout: "2006-01-02", timeLayout: "15:04"},
	}
}
