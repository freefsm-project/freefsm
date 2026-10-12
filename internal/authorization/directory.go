package authorization

import "context"

// DirectoryPerson is deliberately separate from the sealed account projection.
type DirectoryPerson struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Available bool   `json:"available"`
}

func (s *Service) Directory(ctx context.Context, a Actor) ([]DirectoryPerson, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if !p.Allows("directory.view", All) {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,name,email,is_active FROM users WHERE company_id=$1 ORDER BY lower(name),id`, p.CompanyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := []DirectoryPerson{}
	for rows.Next() {
		var person DirectoryPerson
		if err := rows.Scan(&person.ID, &person.Name, &person.Email, &person.Available); err != nil {
			return nil, err
		}
		people = append(people, person)
	}
	return people, rows.Err()
}
