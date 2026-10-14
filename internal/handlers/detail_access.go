package handlers

import (
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func detailPresentation(typ string, policy *services.PolicyService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
			next.ServeHTTP(w, withDetailPresentation(r, typ, id, policy))
		})
	}
}

func withDetailPresentation(r *http.Request, typ string, id int64, policy *services.PolicyService) *http.Request {
	access := templates.DetailAccess{}
	ctx := r.Context()
	req, ok := authorization.FromContext(ctx)
	if !ok {
		return r.WithContext(templates.WithDetailAccess(ctx, access))
	}
	p, err := req.Service.Principal(ctx, req.Actor)
	if err != nil || req.Service.ParentAccess(ctx, req.Actor, typ, id, "") != nil {
		return r.WithContext(templates.WithDetailAccess(ctx, access))
	}
	ref := objectref.New(objectref.Type(typ), id)
	// Existing command policy is a capability intersection, never a substitute
	// for the independently granted action. We do not synthesize office authority.
	legacyUpdate, legacyDelete := false, false
	if policy != nil {
		// Supply the authenticated account's recorded discriminator to the existing
		// command adapter; no UI decision compares or infers authority from labels.
		if u, ok := middleware.UserFromContext(ctx); ok && u != nil {
			legacyUpdate = policy.CanAccessObject(ctx, p.UserID, u.Role, ref, services.PolicyUpdate)
			legacyDelete = policy.CanAccessObject(ctx, p.UserID, u.Role, ref, services.PolicyDelete)
		}
	}
	action := func(permission string) bool {
		return req.Service.ParentAccess(ctx, req.Actor, typ, id, permission) == nil
	}
	access["edit"] = req.Service.DetailsAccess(ctx, req.Actor, typ, id) == nil
	access["archive"] = action(typ+".archive") && legacyDelete
	access["restore"] = p.Protected && p.Allows(typ+".restore", authorization.All)
	if typ == "project" || typ == "asset" {
		access["archive"] = action(typ + ".archive")
		access["restore"] = req.Service.MasterRestoreAccess(ctx, req.Actor, typ, id) == nil
	}
	access["status"] = action(typ + ".status")
	access["pricing"] = req.Service.ParentFacet(ctx, req.Actor, typ, id, "pricing.view") == nil
	access["settlement"] = req.Service.ParentFacet(ctx, req.Actor, typ, id, "settlement.view") == nil
	if typ == "job" {
		access["relations"] = action("job.relations.edit")
		access["time"] = p.Allows("time.view", authorization.Own)
		access["create-next"] = action("job.create") && legacyUpdate
		access["create-invoice"] = policy != nil && policy.CanCreateDocumentForJob(ctx, id, objectref.TypeInvoice)
		access["create-estimate"] = policy != nil && policy.CanCreateDocumentForJob(ctx, id, objectref.TypeEstimate)
		access["clock-in"] = p.Allows("time.clock_in", authorization.Own)
		access["clock-out"] = p.Allows("time.clock_out", authorization.Own)
		access["subtask-complete"] = action("job.subtask.complete") && legacyUpdate
		access["subtask-reopen"] = action("job.subtask.reopen") && legacyUpdate
	}
	if typ == "estimate" || typ == "invoice" {
		access["pdf"] = req.Service.PDFPermission(ctx, req.Actor, typ, id, "pdf.generate") == nil || req.Service.PDFPermission(ctx, req.Actor, typ, id, "pdf.download") == nil || req.Service.PDFPermission(ctx, req.Actor, typ, id, "delivery.request") == nil
		access["delivery-retry"] = action(typ+".delivery.request") && legacyUpdate
		access["convert"] = action("estimate.convert")
		access["revert"] = action("invoice.revert")
		access["finalize"] = action("invoice.finalize") && legacyDelete
		access["payment"] = action("payment.record") && legacyDelete
		access["apply-credit"] = p.Allows("credit.apply", authorization.All) && p.Allows("credit.view", authorization.All) && access["settlement"] && legacyDelete
		access["reverse-payment"] = p.Allows("payment.reverse", authorization.All) && access["settlement"] && legacyDelete
		access["reverse-application"] = p.Allows("credit.application.reverse", authorization.All) && access["settlement"] && legacyDelete
	}
	return r.WithContext(templates.WithDetailAccess(ctx, access))
}
