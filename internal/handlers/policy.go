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
