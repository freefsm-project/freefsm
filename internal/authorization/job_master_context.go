package authorization

import (
	"context"
	"encoding/json"
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
	Contact  *JobContactContext  `json:"contact,omitempty"`
	Location *JobLocationContext `json:"location,omitempty"`
}

func (s *Service) JobMasters(ctx context.Context, a Actor, jobID int64) (JobMasterContext, error) {
	var out JobMasterContext
	p, err := s.Principal(ctx, a)
	if err != nil {
		return out, err
	}
	if !p.Allows("job.view", Assigned) {
		return out, ErrNotFound
	}
	var contact, location []byte
	err = s.Pool.QueryRow(ctx, `SELECT
 CASE WHEN $4 AND c.id IS NOT NULL THEN jsonb_build_object('id',c.id,'name',trim(c.first_name||' '||c.last_name),'role_title',c.role_title,'phone',c.phone,'email',c.email,'service_notes',c.service_notes) END,
 CASE WHEN $5 AND l.id IS NOT NULL THEN jsonb_build_object('id',l.id,'title',l.title,'address_1',l.address_1,'address_2',l.address_2,'city',l.city,'state',l.state,'zip_code',l.zip_code,'latitude',l.latitude,'longitude',l.longitude,'access_notes',l.access_notes,'service_notes',l.service_notes) END
 FROM jobs j JOIN job_assignments a ON a.job_id=j.id
 LEFT JOIN customer_contacts c ON c.id=j.customer_contact_id AND c.customer_id=j.customer_id AND c.company_id=j.company_id AND c.deleted_at IS NULL
 LEFT JOIN locations l ON l.id=j.location_id AND l.object_type='customer' AND l.object_id=j.customer_id AND l.company_id=j.company_id AND l.deleted_at IS NULL
 WHERE j.id=$1 AND j.company_id=$2 AND a.user_id=$3 AND j.deleted_at IS NULL`, jobID, p.CompanyID, a.UserID, p.Allows("contact.view", Context), p.Allows("location.view", Context)).Scan(&contact, &location)
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
	return out, nil
}
