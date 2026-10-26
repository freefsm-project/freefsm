package authorization

import "context"

func fieldPDFAnchor(ctx context.Context, q queryer, kind string, id int64) error {
	var linked bool
	if err := q.QueryRow(ctx, `SELECT job_id IS NOT NULL FROM `+kind+`s WHERE id=$1`, id).Scan(&linked); err != nil {
		return err
	}
	if !linked {
		return ErrNotFound
	}
	return nil
}
