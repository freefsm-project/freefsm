package handlers

import (
	"context"
	"net/http"

	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func New(db *pgxpool.Pool, entClient *ent.Client, sessions *services.SessionService, cfg *config.Config) http.Handler {
	r := chi.NewRouter()

	userService := services.NewUserService(entClient)
	userFn := func(ctx context.Context, userID int64) (*middleware.UserInfo, error) {
		u, err := userService.GetByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		return &middleware.UserInfo{
			ID:    u.ID,
			Name:  u.Name,
			Email: u.Email,
			Role:  u.Role,
		}, nil
	}
	authMW := middleware.Auth(sessions, userFn)

	customerService := services.NewCustomerService(entClient)
	statusService := services.NewStatusService(entClient)
	jobService := services.NewJobService(entClient)
	itemService := services.NewItemService(entClient)
	contactSvc := services.NewCustomerContactService(entClient)
	projectSvc := services.NewProjectService(entClient)
	locationSvc := services.NewLocationService(entClient)
	customerHandler := NewCustomerHandler(customerService, contactSvc)
	itemHandler := NewItemHandler(itemService)
	jobHandler := NewJobHandler(jobService, customerService, statusService, projectSvc, locationSvc, contactSvc)
	estimateHandler := NewEstimateHandler(services.NewEstimateService(entClient), customerService, jobService, statusService, itemService)
	invoiceHandler := NewInvoiceHandler(services.NewInvoiceService(entClient), customerService, jobService, statusService, itemService)

	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Get("/", handleDashboard)
		r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
			handleLogout(w, r, sessions)
		})
		r.Get("/customers", customerHandler.List)
		r.Get("/customers/new", customerHandler.Create)
		r.Post("/customers", customerHandler.Create)
		r.Get("/customers/{id}", customerHandler.Show)
		r.Get("/customers/{id}/edit", customerHandler.Update)
		r.Post("/customers/{id}", customerHandler.Update)
		r.Post("/customers/{id}/delete", customerHandler.Delete)
		r.Get("/customers/{id}/contacts", customerHandler.Contacts)
		r.Route("/items", func(r chi.Router) {
			r.Get("/", itemHandler.List)
			r.Post("/", itemHandler.Create)
			r.Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
				if chi.URLParam(r, "id") == "new" {
					itemHandler.Create(w, r)
					return
				}
				itemHandler.Show(w, r)
			})
			r.Get("/{id}/edit", itemHandler.Update)
			r.Post("/{id}", itemHandler.Update)
			r.Post("/{id}/delete", itemHandler.Delete)
		})
		r.Get("/jobs", jobHandler.List)
		r.Get("/jobs/new", jobHandler.Create)
		r.Post("/jobs", jobHandler.Create)
		r.Get("/jobs/{id}", jobHandler.Show)
		r.Get("/jobs/{id}/edit", jobHandler.Update)
		r.Post("/jobs/{id}", jobHandler.Update)
		r.Post("/jobs/{id}/delete", jobHandler.Delete)
		r.Get("/estimates", estimateHandler.List)
		r.Get("/estimates/new", estimateHandler.Create)
		r.Post("/estimates", estimateHandler.Create)
		r.Get("/estimates/{id}", estimateHandler.Show)
		r.Get("/estimates/{id}/edit", estimateHandler.Update)
		r.Post("/estimates/{id}", estimateHandler.Update)
		r.Post("/estimates/{id}/delete", estimateHandler.Delete)
		r.Get("/invoices", invoiceHandler.List)
		r.Get("/invoices/new", invoiceHandler.Create)
		r.Post("/invoices", invoiceHandler.Create)
		r.Get("/invoices/{id}", invoiceHandler.Show)
		r.Get("/invoices/{id}/edit", invoiceHandler.Update)
		r.Post("/invoices/{id}", invoiceHandler.Update)
		r.Post("/invoices/{id}/delete", invoiceHandler.Delete)
		r.Post("/invoices/{id}/payments", invoiceHandler.RecordPayment)
	})

	authHandler := NewAuthHandler(db, sessions)
	r.Get("/login", authHandler.ServeHTTP)
	r.Post("/login", authHandler.ServeHTTP)

	setupHandler := NewSetupHandler(db, sessions, cfg)
	r.Get("/setup", setupHandler.ServeHTTP)
	r.Post("/setup", setupHandler.ServeHTTP)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	return r
}
