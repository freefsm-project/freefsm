package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// TestMail holds the authority mutex through handoff. SMTP is an external
// at-least-once side effect: failure to record a post-handoff result is not proof
// that the provider rejected it. The caller must present that ambiguity.
func (s *Service) TestMail(ctx context.Context, a Actor, send func() error) error {
	var result error
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("configuration.mail.test", Configuration) {
			return ErrForbidden
		}
		if err := audit(ctx, tx, a, p, "configuration.mail.test", Configuration, "company_settings", p.CompanyID, map[string]string{"phase": "submission_requested"}); err != nil {
			return err
		}
		result = send()
		outcome := "accepted_by_transport"
		if result != nil {
			outcome = "not_confirmed"
		}
		return audit(ctx, tx, a, p, "configuration.mail.test", Configuration, "company_settings", p.CompanyID, map[string]string{"phase": outcome})
	})
	if err != nil {
		return err
	}
	return result
}

// AuthorizeEmailEnable retains the old adapter name but owns the complete action.
func (s *Service) AuthorizeEmailEnable(ctx context.Context, a Actor) error {
	return s.EnableEmail(ctx, a)
}
