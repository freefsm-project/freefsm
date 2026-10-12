// Package vocabulary defines the shared identity rules for reusable definitions.
package vocabulary

//go:generate go run ./cmd/generate

import (
	"strings"

	"golang.org/x/text/cases"
)

// NameKey folds case and trims outer Unicode whitespace without conflating
// spelling, internal whitespace, accents or different definition identities.
func NameKey(name string) string {
	return cases.Fold().String(strings.TrimSpace(name))
}
