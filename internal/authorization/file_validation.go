package authorization

// FileValidationError identifies rejected file content or transfer input. Storage
// and audit errors must not use this type: they leave operation identity retryable.
// Message is presentation only and does not determine receipt disposition.
type FileValidationError struct {
	Message string
}

func (e *FileValidationError) Error() string { return e.Message }
