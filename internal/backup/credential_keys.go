package backup

import (
	"context"
	"strings"

	"github.com/freefsm-project/freefsm/internal/credentials"
)

// Only already-encrypted credentials enter the authenticated, encrypted archive
// manifest. They are never copied into operation status or diagnostic output.
func (m *Manager) providerCredentials(ctx context.Context) ([]string, error) {
	c, err := m.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, `SELECT coalesce(to_jsonb(s)->>'smtp_password','') FROM company_settings s`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if strings.HasPrefix(value, "aesgcm:") {
			result = append(result, value)
		}
	}
	return result, rows.Err()
}

func verifyProviderKeys(values []string) error {
	for _, value := range values {
		if _, err := credentials.Open(value); err != nil {
			return engineFailure(FailureProviderKey, "provider credential key unavailable")
		}
	}
	return nil
}
