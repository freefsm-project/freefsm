package services

import (
	"github.com/freefsm-project/freefsm/internal/ent"
	"strings"
	"testing"
)

func TestPasswordPolicyMatchesJavaScript(t *testing.T) {
	s := NewUserService(nil)
	for _, tc := range []struct {
		name, password string
		policy         *ent.CompanySettings
		valid          bool
	}{
		{"UTF16 not bytes", "Aa1!éé", nil, false},
		{"surrogate pairs", "Aa1!😀😀", nil, true},
		{"ASCII uppercase", "Éabcdef1!", nil, false},
		{"ASCII digit", "Abcdefg١!", nil, false},
		{"ASCII nonalphanumeric special", "Abcdef1é", nil, true},
		{"bcrypt exact bytes", "Aa1!" + strings.Repeat("x", 68), nil, true},
		{"bcrypt over bytes", "Aa1!" + strings.Repeat("x", 69), nil, false},
		{"multibyte bcrypt bytes", "Aa1!" + strings.Repeat("😀", 18), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.ValidatePassword(tc.password, tc.policy); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
