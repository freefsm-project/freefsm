package handlers

import (
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

const (
	policyRead       = services.PolicyRead
	policyCreate     = services.PolicyCreate
	policyUpdate     = services.PolicyUpdate
	policyDelete     = services.PolicyDelete
	policyAttachFile = services.PolicyAttachFile
)

func isAdminOrDispatcher(u *middleware.UserInfo) bool {
	return u != nil && (u.Role == "admin" || u.Role == "dispatcher")
}
