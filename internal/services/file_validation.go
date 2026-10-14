package services

import (
	"fmt"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Mark only domain validation at its origin, never storage or audit failures.
func invalidFile(format string, args ...any) error {
	return &authorization.FileValidationError{Message: fmt.Sprintf(format, args...)}
}
