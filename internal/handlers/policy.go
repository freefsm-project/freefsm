package handlers

import "github.com/MartialM1nd/freefsm/internal/middleware"

const (
	policyRead       = "read"
	policyCreate     = "create"
	policyUpdate     = "update"
	policyDelete     = "delete"
	policyAttachFile = "attach_file"
)

func isAdminOrDispatcher(u *middleware.UserInfo) bool {
	return u != nil && (u.Role == "admin" || u.Role == "dispatcher")
}

func canAccessObject(u *middleware.UserInfo, objectType string, objectID int64, action string) bool {
	if u == nil || objectID <= 0 {
		return false
	}
	if u.Role == "admin" {
		return true
	}
	if u.Role == "dispatcher" {
		switch objectType {
		case "customer", "job", "project", "estimate", "invoice", "asset", "item", "time_entry":
			return true
		default:
			return false
		}
	}

	// Until job assignments store user IDs, tech object access cannot be proven safely.
	switch objectType {
	case "item":
		return action == policyRead
	default:
		return false
	}
}
