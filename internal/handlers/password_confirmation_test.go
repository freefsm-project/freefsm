package handlers

import "testing"

func TestValidatePasswordConfirmation(t *testing.T) {
	tests := []struct {
		name         string
		password     string
		confirmation string
		want         error
		wantMessage  string
	}{
		{name: "password required", confirmation: "Password1!", want: errPasswordRequired, wantMessage: "password is required"},
		{name: "confirmation required", password: "Password1!", want: errPasswordConfirmationRequired, wantMessage: "password confirmation is required"},
		{name: "exact match required", password: "Password1!", confirmation: "password1!", want: errPasswordsDoNotMatch, wantMessage: "passwords do not match"},
		{name: "matching", password: "Password1!", confirmation: "Password1!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validatePasswordConfirmation(tt.password, tt.confirmation); got != tt.want {
				t.Fatalf("validatePasswordConfirmation() = %v, want %v", got, tt.want)
			} else if got != nil && got.Error() != tt.wantMessage {
				t.Fatalf("validatePasswordConfirmation() message = %q, want %q", got.Error(), tt.wantMessage)
			}
		})
	}
}
