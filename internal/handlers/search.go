package handlers

import (
	"net/http"
	"strings"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

type SearchHandler struct {
	svc *services.SearchService
}

func NewSearchHandler(svc *services.SearchService) *SearchHandler {
	return &SearchHandler{svc: svc}
}

func (h *SearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	customers, jobs, projects, invoices, estimates, err := h.svc.Search(r.Context(), q, 10, u.ID, u.Role)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	data := templates.SearchPageData{
		Query:     q,
		Customers: customers,
		Jobs:      jobs,
		Projects:  projects,
		Invoices:  invoices,
		Estimates: estimates,
	}

	templates.SearchResults(data).Render(r.Context(), w)
}
