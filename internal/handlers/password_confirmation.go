package handlers

import "errors"

var (
	errPasswordRequired             = errors.New("password is required")
	errPasswordConfirmationRequired = errors.New("password confirmation is required")
	errPasswordsDoNotMatch          = errors.New("passwords do not match")
)

func validatePasswordConfirmation(password, confirmation string) error {
	if password == "" {
		return errPasswordRequired
	}
	if confirmation == "" {
		return errPasswordConfirmationRequired
	}
	if password != confirmation {
		return errPasswordsDoNotMatch
	}
	return nil
}
