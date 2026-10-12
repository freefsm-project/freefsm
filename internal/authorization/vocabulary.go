package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/vocabulary"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrVocabularyInvalid = errors.New("invalid vocabulary input")
var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type VocabularyInput struct {
	Name      string
	Color     string
	SortOrder int
}

func vocabularyTable(kind string) (string, string, error) {
	switch kind {
	case "tag":
		return "tags", "tags", nil
	case "asset_type":
		return "asset_types", "assets", nil
	case "asset_status":
		return "asset_statuses", "assets", nil
	}
	return "", "", ErrVocabularyInvalid
}

// ChangeVocabulary commits the definition, before/after history and activity
// under the shared authority mutex. No parent authority is implied here.
func (s *Service) ChangeVocabulary(ctx context.Context, a Actor, kind string, id int64, operation string, in VocabularyInput) (int64, error) {
	table, domain, err := vocabularyTable(kind)
	if err != nil {
		return 0, err
	}
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := "configuration." + domain + ".manage"
		if !p.Allows(permission, Configuration) {
			return ErrForbidden
		}
		var before, after json.RawMessage
		if id != 0 {
			query := `SELECT to_jsonb(v) FROM ` + table + ` v WHERE id=$1`
			args := []any{id}
			if kind == "tag" {
				query += ` AND company_id=$2`
				args = append(args, p.CompanyID)
			}
			if err := tx.QueryRow(ctx, query+` FOR UPDATE`, args...).Scan(&before); err == pgx.ErrNoRows {
				return ErrNotFound
			} else if err != nil {
				return err
			}
		}
		switch operation {
		case "save":
			in.Name = strings.TrimSpace(in.Name)
			if in.Name == "" || !utf8.ValidString(in.Name) {
				return ErrVocabularyInvalid
			}
			if err := checkVocabularyName(ctx, tx, table, kind, p.CompanyID, id, in.Name); err != nil {
				return err
			}
			if in.Color == "" {
				in.Color = "#3B82F6"
			}
			if kind != "asset_type" && !colorPattern.MatchString(in.Color) {
				return ErrVocabularyInvalid
			}
			if id == 0 {
				if kind == "tag" {
					err = tx.QueryRow(ctx, `INSERT INTO tags(company_id,name,color) VALUES($1,$2,$3) RETURNING id`, p.CompanyID, in.Name, in.Color).Scan(&id)
				} else if kind == "asset_type" {
					err = tx.QueryRow(ctx, `INSERT INTO asset_types(company_id,name,sort_order) VALUES($1,$2,$3) RETURNING id`, p.CompanyID, in.Name, in.SortOrder).Scan(&id)
				} else {
					err = tx.QueryRow(ctx, `INSERT INTO asset_statuses(company_id,name,color,sort_order) VALUES($1,$2,$3,$4) RETURNING id`, p.CompanyID, in.Name, in.Color, in.SortOrder).Scan(&id)
				}
			} else if kind == "tag" {
				_, err = tx.Exec(ctx, `UPDATE tags SET name=$2,color=$3 WHERE id=$1`, id, in.Name, in.Color)
			} else if kind == "asset_type" {
				_, err = tx.Exec(ctx, `UPDATE asset_types SET name=$2,sort_order=$3 WHERE id=$1`, id, in.Name, in.SortOrder)
			} else {
				_, err = tx.Exec(ctx, `UPDATE asset_statuses SET name=$2,color=$3,sort_order=$4 WHERE id=$1`, id, in.Name, in.Color, in.SortOrder)
			}
		case "retire", "reactivate":
			if id == 0 {
				return ErrNotFound
			}
			_, err = tx.Exec(ctx, `UPDATE `+table+` SET retired=$2 WHERE id=$1`, id, operation == "retire")
		case "delete":
			if id == 0 {
				return ErrNotFound
			}
			_, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE id=$1`, id)
		default:
			return ErrVocabularyInvalid
		}
		if err != nil {
			return err
		}
		if operation != "delete" {
			if err := tx.QueryRow(ctx, `SELECT to_jsonb(v) FROM `+table+` v WHERE id=$1`, id).Scan(&after); err != nil {
				return err
			}
		}
		detail := map[string]any{"operation": operation, "before": before, "after": after}
		var label struct {
			Name string `json:"name"`
		}
		labelSource := after
		if labelSource == nil {
			labelSource = before
		}
		if err := json.Unmarshal(labelSource, &label); err != nil {
			return err
		}
		detail["entity_name"] = label.Name
		if err := audit(ctx, tx, a, p, permission, Configuration, kind, id, detail); err != nil {
			return err
		}
		metadata, _ := json.Marshal(detail)
		action := operation + "d"
		if operation == "save" {
			action = "updated"
			if before == nil {
				action = "created"
			}
		}
		activityKind := kind
		if kind == "asset_type" {
			activityKind = "type"
		} else if kind == "asset_status" {
			activityKind = "status"
		}
		_, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5,$6)`, p.CompanyID, a.UserID, activityKind+"_"+action, kind, id, metadata)
		return err
	})
	return id, VocabularyError(err)
}

// All production definition writers hold the shared authorization mutex. This
// check makes Unicode identity independent of PostgreSQL encoding/collation;
// the expression indexes additionally guard ordinary SQL writes and FK races.
func checkVocabularyName(ctx context.Context, tx pgx.Tx, table, kind string, companyID, id int64, name string) error {
	query := `SELECT name FROM ` + table + ` WHERE id<>$1`
	args := []any{id}
	if kind == "tag" {
		query += ` AND company_id=$2`
		args = append(args, companyID)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	key := vocabulary.NameKey(name)
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return err
		}
		if vocabulary.NameKey(existing) == key {
			return ErrConflict
		}
	}
	return rows.Err()
}

func VocabularyError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505", "23503", "23001", "23514":
			return fmt.Errorf("%w: vocabulary name, selection or reference conflict", ErrConflict)
		}
	}
	return err
}

// TagParentAccess is deliberately closed to the six currently supported parents.
// Assigned scope is only meaningful for Jobs and Job-bound documents.
func tagParentAccess(ctx context.Context, q queryer, p Principal, kind string, id int64, permission string) (Scope, error) {
	table := ""
	switch kind {
	case "customer":
		table = "customers"
	case "project":
		table = "projects"
	case "job":
		table = "jobs"
	case "asset":
		table = "assets"
	case "estimate":
		table = "estimates"
	case "invoice":
		table = "invoices"
	default:
		return "", ErrNotFound
	}
	var active bool
	var jobID int64
	jobExpr := "0"
	if kind == "job" {
		jobExpr = "id"
	} else if kind == "estimate" || kind == "invoice" {
		jobExpr = "coalesce(job_id,0)"
	}
	visible := ""
	if kind == "estimate" || kind == "invoice" {
		visible = " AND conversion_hidden_at IS NULL"
	}
	if err := q.QueryRow(ctx, `SELECT deleted_at IS NULL,`+jobExpr+` FROM `+table+` WHERE id=$1 AND company_id=$2`+visible+` FOR UPDATE`, id, p.CompanyID).Scan(&active, &jobID); err == pgx.ErrNoRows {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	allVisible := p.Allows(kind+".view", All)
	if !active {
		if allVisible && p.Allows(kind+".archived.view", All) {
			return "", ErrForbidden
		}
		return "", ErrNotFound
	}
	assigned := false
	if jobID > 0 && p.Allows(kind+".view", Assigned) && (!allVisible || !p.Allows(permission, All)) {
		var userID int64
		err := q.QueryRow(ctx, `SELECT a.user_id FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=$1 AND j.deleted_at IS NULL AND a.user_id=$2 FOR SHARE OF j,a`, jobID, p.UserID).Scan(&userID)
		if err != nil && err != pgx.ErrNoRows {
			return "", err
		}
		assigned = err == nil
	}
	// Visibility is resolved before the independent action scope. An All-visible
	// parent outside Assigned action scope is forbidden, never disguised as absent.
	if !allVisible && !assigned {
		return "", ErrNotFound
	}
	if p.Allows(permission, All) {
		return All, nil
	}
	if assigned && p.Allows(permission, Assigned) {
		return Assigned, nil
	}
	return "", ErrForbidden
}

func (s *Service) ChangeTagLink(ctx context.Context, a Actor, kind string, id, tagID int64, attach bool) error {
	return VocabularyError(s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		permission := "tag.detach"
		if attach {
			permission = "tag.attach"
		}
		scope, err := tagParentAccess(ctx, tx, p, kind, id, permission)
		if err != nil {
			return err
		}
		if !p.Allows(permission, scope) {
			return ErrForbidden
		}
		var name string
		var retired bool
		if err := tx.QueryRow(ctx, `SELECT name,retired FROM tags WHERE id=$1 AND company_id=$2 FOR SHARE`, tagID, p.CompanyID).Scan(&name, &retired); err == pgx.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if attach {
			if retired {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,$3,$4)`, p.CompanyID, tagID, kind, id)
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM tag_links WHERE company_id=$1 AND tag_id=$2 AND object_type=$3 AND object_id=$4`, p.CompanyID, tagID, kind, id)
		}
		if err != nil {
			return err
		}
		detail := map[string]any{"tag_id": tagID, "tag_name": name}
		if err := audit(ctx, tx, a, p, permission, scope, kind, id, detail); err != nil {
			return err
		}
		metadata, _ := json.Marshal(detail)
		_, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5,$6)`, p.CompanyID, a.UserID, strings.ReplaceAll(permission, ".", "_")+"ed", kind, id, metadata)
		return err
	}))
}

func (s *Service) TagActionAllowed(ctx context.Context, a Actor, kind string, id int64, action string) bool {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return false
	}
	scope, err := tagParentAccess(ctx, s.Pool, p, kind, id, "tag."+action)
	return err == nil && p.Allows("tag."+action, scope)
}
