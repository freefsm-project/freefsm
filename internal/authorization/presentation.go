package authorization

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Presentation is intrinsic Own access, independent of directory or Role grants.
func (s *Service) Presentation(ctx context.Context, a Actor, legacy string) (string, error) {
	var theme string
	err := s.Pool.QueryRow(ctx, `SELECT theme FROM user_presentation WHERE user_id=$1`, a.UserID).Scan(&theme)
	if err == pgx.ErrNoRows {
		if legacy != "light" && legacy != "dark" {
			return "system", nil
		}
		// First explicit legacy choice wins; another device must not overwrite it.
		err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
			tag, err := tx.Exec(ctx, `INSERT INTO user_presentation(user_id,theme) VALUES($1,$2) ON CONFLICT DO NOTHING`, a.UserID, legacy)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				if err := audit(ctx, tx, a, p, "presentation.import", Own, "user", a.UserID, map[string]string{"theme": legacy}); err != nil {
					return err
				}
			}
			return tx.QueryRow(ctx, `SELECT theme FROM user_presentation WHERE user_id=$1`, a.UserID).Scan(&theme)
		})
		// A browser preference must not prevent the explicit legacy cutover or
		// forced credential replacement. Preserve the cookie's presentation only;
		// defer durable import until an ordinary authenticated identity is ready.
		if errors.Is(err, ErrPending) || errors.Is(err, ErrForbidden) {
			return legacy, nil
		}
	}
	return theme, err
}

func (s *Service) SavePresentation(ctx context.Context, a Actor, theme, font string) error {
	if theme != "system" && theme != "light" && theme != "dark" {
		return fmt.Errorf("choose System, Light or Dark")
	}
	if font != "small" && font != "medium" && font != "large" {
		return fmt.Errorf("choose Small, Medium or Large")
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_presentation(user_id,theme) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET theme=excluded.theme`, a.UserID, theme); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET font_size=$2 WHERE id=$1`, a.UserID, font); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "presentation.edit", Own, "user", a.UserID, map[string]string{"theme": theme, "font_size": font})
	})
}
