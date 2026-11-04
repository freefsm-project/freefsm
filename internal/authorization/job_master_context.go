package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type JobContactContext struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	RoleTitle    string `json:"role_title"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	ServiceNotes string `json:"service_notes"`
}
type JobLocationContext struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	Address1     string   `json:"address_1"`
	Address2     string   `json:"address_2"`
	City         string   `json:"city"`
	State        string   `json:"state"`
	ZipCode      string   `json:"zip_code"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
	AccessNotes  string   `json:"access_notes"`
	ServiceNotes string   `json:"service_notes"`
}
type JobMasterContext struct {
	Contacts         []JobContactContext       `json:"contacts,omitempty"`
	Asset            *JobAssetContext          `json:"asset,omitempty"`
	Assets           []JobAssetContext         `json:"assets,omitempty"`
	Contact          *JobContactContext        `json:"contact,omitempty"`
	Location         *JobLocationContext       `json:"location,omitempty"`
	Project          *JobProjectContext        `json:"project,omitempty"`
	ProjectReference *ArchivedProjectReference `json:"project_reference,omitempty"`
}

// ArchivedProjectReference preserves an explicit relationship without granting
// operational detail or navigation. Projects have names, not document numbers.
type ArchivedProjectReference struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// This allowlist deliberately has no master fields, internal Notes, Jobs,
// collaboration or Customer links. It is only reached through an assigned Job.
type JobProjectContext struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Status           string  `json:"status"`
	Progress         float64 `json:"progress"`
	PlannedStartDate *string `json:"planned_start_date"`
	PlannedEndDate   *string `json:"planned_end_date"`
	Location         string  `json:"location"`
	LocationArchived bool    `json:"location_archived,omitempty"`
	ServiceNotes     string  `json:"service_notes"`
}

// JobAssetContext is deliberately not an Asset entity. The explicit allowlist
// excludes master Custom Fields, collaboration, audit and association history.
type JobAssetContext struct {
	LeaseCoverage    *FieldLeaseCoverage   `json:"lease_coverage,omitempty"`
	ServiceEntryPath string                `json:"-"`
	ServiceEntries   []ServiceEntry        `json:"service_entries,omitempty"`
	ID               int64                 `json:"id"`
	Number           string                `json:"number"`
	Name             string                `json:"name"`
	OwnershipClass   string                `json:"ownership_class"`
	Type             string                `json:"type"`
	Status           string                `json:"status"`
	SerialNumber     string                `json:"serial_number"`
	Manufacturer     string                `json:"manufacturer"`
	Model            string                `json:"model"`
	InstalledAt      *time.Time            `json:"installed_at"`
	WarrantyExpires  *time.Time            `json:"warranty_expires"`
	Location         *AssetLocationContext `json:"location"`
	ServiceNotes     string                `json:"service_notes"`
}
type AssetLocationContext struct {
	ID       int64  `json:"id,omitempty"`
	Title    string `json:"title"`
	Archived bool   `json:"archived,omitempty"`
}

func (s *Service) JobMasters(ctx context.Context, a Actor, jobID int64) (JobMasterContext, error) {
	var out JobMasterContext
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		var err error
		out, err = jobMasters(ctx, tx, p, a, jobID)
		return err
	})
	return out, err
}

// JobCustomerAndMasters reads the combined page under one authority fence so
// Customer and linked facets cannot come from different grant generations.
func (s *Service) JobCustomerAndMasters(ctx context.Context, a Actor, jobID int64) (CustomerContext, JobMasterContext, error) {
	var customer CustomerContext
	var masters JobMasterContext
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		var err error
		customer, err = jobCustomer(ctx, tx, p, a, jobID)
		if err != nil {
			return err
		}
		masters, err = jobMasters(ctx, tx, p, a, jobID)
		return err
	})
	return customer, masters, err
}

func jobMasters(ctx context.Context, q queryer, p Principal, a Actor, jobID int64) (JobMasterContext, error) {
	var out JobMasterContext
	if !p.Allows("job.view", Assigned) {
		return out, ErrNotFound
	}
	var contact, location, project, projectReference []byte
	err := q.QueryRow(ctx, `SELECT
 CASE WHEN $4 AND c.id IS NOT NULL THEN jsonb_build_object('id',c.id,'name',trim(c.first_name||' '||c.last_name),'role_title',c.role_title,'phone',c.phone,'email',c.email,'service_notes',c.service_notes) END,
 CASE WHEN $5 AND l.id IS NOT NULL THEN jsonb_build_object('id',l.id,'title',l.title,'address_1',l.address_1,'address_2',l.address_2,'city',l.city,'state',l.state,'zip_code',l.zip_code,'latitude',l.latitude,'longitude',l.longitude,'access_notes',l.access_notes,'service_notes',l.service_notes) END,
 CASE WHEN $6 AND p.id IS NOT NULL AND (p.deleted_at IS NULL OR $7) THEN jsonb_build_object('name',p.name,'description',p.description,'status',coalesce(ps.name,''),'progress',CASE WHEN ps.category_key='project:completed' THEN 100 ELSE p.completion_percentage END,'planned_start_date',p.planned_start_date::text,'planned_end_date',p.planned_end_date::text,'location',coalesce(pl.title,''),'location_archived',pl.deleted_at IS NOT NULL,'service_notes',p.service_notes) END,
 CASE WHEN $6 AND p.deleted_at IS NOT NULL THEN jsonb_build_object('name',p.name,'archived',true) END
 FROM jobs j JOIN job_assignments a ON a.job_id=j.id
 LEFT JOIN customer_contacts c ON c.id=j.customer_contact_id AND c.customer_id=j.customer_id AND c.company_id=j.company_id AND c.deleted_at IS NULL
 LEFT JOIN locations l ON l.id=j.location_id AND l.object_type='customer' AND l.object_id=j.customer_id AND l.company_id=j.company_id AND l.deleted_at IS NULL
 LEFT JOIN projects p ON p.id=j.project_id AND p.customer_id=j.customer_id AND p.company_id=j.company_id
 LEFT JOIN statuses ps ON ps.id=p.status_id
 LEFT JOIN locations pl ON pl.id=p.location_id AND pl.object_type='customer' AND pl.object_id=p.customer_id AND pl.company_id=p.company_id
 WHERE j.id=$1 AND j.company_id=$2 AND a.user_id=$3 AND j.deleted_at IS NULL`, jobID, p.CompanyID, a.UserID, p.Allows("contact.view", Context), p.Allows("location.view", Context), p.Allows("project.view", Context), p.Allows("project.archived.view", All)).Scan(&contact, &location, &project, &projectReference)
	if err == pgx.ErrNoRows {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if len(contact) > 0 {
		out.Contact = &JobContactContext{}
		if err = json.Unmarshal(contact, out.Contact); err != nil {
			return out, err
		}
	}
	if len(location) > 0 {
		out.Location = &JobLocationContext{}
		if err = json.Unmarshal(location, out.Location); err != nil {
			return out, err
		}
	}
	if len(project) > 0 {
		out.Project = &JobProjectContext{}
		if err = json.Unmarshal(project, out.Project); err != nil {
			return out, err
		}
	}
	if len(projectReference) > 0 {
		out.ProjectReference = &ArchivedProjectReference{}
		if err = json.Unmarshal(projectReference, out.ProjectReference); err != nil {
			return out, err
		}
	}
	if p.Allows("contact.view", Context) {
		rows, err := q.Query(ctx, `SELECT c.id,trim(c.first_name||' '||c.last_name),c.role_title,c.phone,c.email,c.service_notes FROM job_contacts jc JOIN jobs j ON j.id=jc.job_id JOIN customer_contacts c ON c.id=jc.contact_id AND c.customer_id=j.customer_id AND c.company_id=j.company_id WHERE j.id=$1 AND j.company_id=$2 AND j.deleted_at IS NULL AND c.deleted_at IS NULL AND EXISTS(SELECT 1 FROM job_assignments WHERE job_id=j.id AND user_id=$3) ORDER BY c.id`, jobID, p.CompanyID, a.UserID)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var c JobContactContext
			if err := rows.Scan(&c.ID, &c.Name, &c.RoleTitle, &c.Phone, &c.Email, &c.ServiceNotes); err != nil {
				return out, err
			}
			out.Contacts = append(out.Contacts, c)
		}
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	if p.Allows("asset.view", Context) {
		rows, err := q.Query(ctx, `SELECT jsonb_build_object('id',equipment.id,'number',equipment.number,'name',equipment.name,'ownership_class',equipment.ownership_class,'type',typ.name,'status',coalesce(st.name,''),'serial_number',equipment.serial_number,'manufacturer',equipment.manufacturer,'model',equipment.model,'installed_at',equipment.installed_at,'warranty_expires',equipment.warranty_expires,'service_notes',equipment.notes,'location',CASE WHEN place.id IS NOT NULL THEN CASE WHEN place.deleted_at IS NOT NULL THEN jsonb_build_object('title',place.title,'archived',true) ELSE jsonb_build_object('id',place.id,'title',place.title) END END)
 FROM jobs j JOIN job_asset_links al ON al.job_id=j.id AND al.operational AND al.unlinked_at IS NULL
 JOIN assets equipment ON equipment.id=al.asset_id AND equipment.company_id=j.company_id AND equipment.customer_id=j.customer_id AND equipment.deleted_at IS NULL
 JOIN asset_types typ ON typ.id=equipment.asset_type_id LEFT JOIN asset_statuses st ON st.id=equipment.asset_status_id
 LEFT JOIN locations place ON place.id=equipment.location_id AND place.object_type='customer' AND place.object_id=j.customer_id AND place.company_id=j.company_id
 WHERE j.id=$1 AND j.company_id=$2 AND j.deleted_at IS NULL AND EXISTS(SELECT 1 FROM job_assignments WHERE job_id=j.id AND user_id=$3) ORDER BY al.id`, jobID, p.CompanyID, a.UserID)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var v JobAssetContext
			if err = rows.Scan(&raw); err != nil {
				return out, err
			}
			if err = json.Unmarshal(raw, &v); err != nil {
				return out, err
			}
			out.Assets = append(out.Assets, v)
		}
		if err = rows.Err(); err != nil {
			return out, err
		}
		var customer int64
		if err = q.QueryRow(ctx, `SELECT customer_id FROM jobs WHERE id=$1`, jobID).Scan(&customer); err != nil {
			return out, err
		}
		for i := range out.Assets {
			out.Assets[i].LeaseCoverage, err = fieldLeaseCoverage(ctx, q, p.CompanyID, customer, out.Assets[i].ID)
			if err != nil {
				return out, err
			}
		}
		if p.Allows("service_entry.view", Assigned) {
			for i := range out.Assets {
				out.Assets[i].ServiceEntryPath = fmt.Sprintf("/jobs/%d/assets/%d/service-entries", jobID, out.Assets[i].ID)
				out.Assets[i].ServiceEntries, err = serviceEntryHistory(ctx, q, out.Assets[i].ID, customer)
				if err != nil {
					return out, err
				}
				for j := range out.Assets[i].ServiceEntries {
					out.Assets[i].ServiceEntries[j].LeaseCoverage = out.Assets[i].LeaseCoverage
				}
			}
		}
		// Retain the pre-v1 singleton representation as an additive compatibility
		// projection of canonical operational links, never of jobs.asset_id.
		if len(out.Assets) > 0 {
			out.Asset = &out.Assets[0]
		}
	}
	return out, nil
}
