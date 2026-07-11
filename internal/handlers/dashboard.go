package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type DashboardHandler struct {
	dashboardSvc *services.DashboardService
	timeEntrySvc *services.TimeEntryService
}

func NewDashboardHandler(dashboardSvc *services.DashboardService, timeEntrySvc *services.TimeEntryService) *DashboardHandler {
	return &DashboardHandler{dashboardSvc: dashboardSvc, timeEntrySvc: timeEntrySvc}
}

func (h *DashboardHandler) Index(w http.ResponseWriter, r *http.Request) {
	loc := middleware.CompanyLocation(r.Context())
	clockWidget := templates.ClockWidgetData{}
	user, _ := middleware.UserFromContext(r.Context())
	stats := services.DashboardStats{}
	if isAdminOrDispatcher(user) {
		stats, _ = h.dashboardSvc.Stats(r.Context(), loc, middleware.CompanyFromContext(r.Context()))
	}
	if user != nil {
		editDashboard := r.URL.Query().Get("edit_dashboard") == "1"
		var widgets []services.DashboardWidgetView
		if editDashboard {
			widgets, _ = h.dashboardSvc.EditableWidgets(r.Context(), dashboardUser(user))
		} else {
			widgets, _ = h.dashboardSvc.Widgets(r.Context(), dashboardUser(user))
		}
		activeEntry, err := h.timeEntrySvc.GetActiveByUser(r.Context(), user.ID)
		if err == nil && activeEntry != nil {
			duration := services.TimeEntryDuration(activeEntry.ClockIn, time.Time{})
			clockWidget = templates.ClockWidgetData{
				IsClockedIn: true,
				Duration:    duration,
				ClockInTime: displayDateTime(r.Context(), activeEntry.ClockIn),
			}
		}
		templates.DashboardPage(templates.DashboardData{
			Stats:       stats,
			ClockWidget: clockWidget,
			Widgets:     widgets,
			EditMode:    editDashboard,
		}).Render(r.Context(), w)
		return
	}

	templates.DashboardPage(templates.DashboardData{
		Stats:       stats,
		ClockWidget: clockWidget,
	}).Render(r.Context(), w)
}

func (h *DashboardHandler) NewWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	widgets, err := h.dashboardSvc.AvailableWidgets(r.Context(), dashboardUser(user))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	templates.DashboardAddWidgetPage(templates.DashboardAddWidgetData{Widgets: widgets}).Render(r.Context(), w)
}

func (h *DashboardHandler) AddWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.dashboardSvc.AddWidget(r.Context(), dashboardUser(user), r.FormValue("widget_type")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?edit_dashboard=1", http.StatusSeeOther)
}

func (h *DashboardHandler) RemoveWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid widget", http.StatusBadRequest)
		return
	}
	if err := h.dashboardSvc.RemoveWidget(r.Context(), dashboardUser(user), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?edit_dashboard=1", http.StatusSeeOther)
}

func (h *DashboardHandler) ReorderWidget(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(r.FormValue("widget_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid widget", http.StatusBadRequest)
		return
	}
	if err := h.dashboardSvc.ReorderWidget(r.Context(), dashboardUser(user), id, r.FormValue("direction")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?edit_dashboard=1", http.StatusSeeOther)
}

func (h *DashboardHandler) ResetWidgets(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.dashboardSvc.ResetWidgets(r.Context(), dashboardUser(user)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Dashboard reset"), http.StatusSeeOther)
}

func (h *DashboardHandler) SaveCompanyDefaultWidgets(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok || user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if user.Role != "admin" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if err := h.dashboardSvc.SaveCompanyDefaultWidgets(r.Context(), dashboardUser(user)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/?edit_dashboard=1&flash="+url.QueryEscape("Company dashboard default saved"), http.StatusSeeOther)
}

func dashboardUser(user *middleware.UserInfo) services.DashboardUser {
	return services.DashboardUser{ID: user.ID, Role: user.Role, CompanyID: user.CompanyID}
}

func handleLogout(w http.ResponseWriter, r *http.Request, sessions *services.SessionService, activitySvc *services.ActivityService) {
	u, _ := middleware.UserFromContext(r.Context())
	if u != nil && activitySvc != nil {
		activitySvc.Record(r.Context(), u.ID, "logged_out", "user", u.ID, map[string]interface{}{
			"entity_name": u.Name,
			"actor_name":  u.Name,
		})
	}
	cookie, err := r.Cookie("session")
	if err == nil {
		sessions.Delete(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: "session", Value: "", Path: "/", MaxAge: -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
