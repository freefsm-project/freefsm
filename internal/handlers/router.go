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
	tagSvc := services.NewTagService(entClient)
	tagLinkSvc := services.NewTagLinkService(entClient)
	commentSvc := services.NewCommentService(entClient)
	commentHandler := NewCommentHandler(commentSvc, userService)
	defSvc := services.NewCustomFieldDefinitionService(entClient)
	cfHandler := NewCustomFieldHandler(defSvc)
	dashboardHandler := NewDashboardHandler(services.NewDashboardService(entClient))
	customerHandler := NewCustomerHandler(customerService, contactSvc, tagSvc, tagLinkSvc, defSvc)
	itemHandler := NewItemHandler(itemService)
	jobHandler := NewJobHandler(jobService, customerService, statusService, projectSvc, locationSvc, contactSvc, tagSvc, tagLinkSvc, defSvc)
	projectHandler := NewProjectHandler(projectSvc, customerService, statusService, locationSvc, jobService, tagSvc, tagLinkSvc, defSvc)
	scheduleHandler := NewScheduleHandler(jobService, customerService, statusService)
	invoiceService := services.NewInvoiceService(entClient)
	estimateHandler := NewEstimateHandler(services.NewEstimateService(entClient), customerService, jobService, statusService, itemService, invoiceService, tagSvc, tagLinkSvc, defSvc)
	invoiceHandler := NewInvoiceHandler(invoiceService, customerService, jobService, statusService, itemService, tagSvc, tagLinkSvc, defSvc)
	tagHandler := NewTagHandler(tagSvc, tagLinkSvc)
	companySettingsSvc := services.NewCompanySettingsService(entClient)
	emailSvc := services.NewEmailService(companySettingsSvc)
	settingsHandler := NewSettingsHandler(companySettingsSvc, emailSvc)
	userHandler := NewUserHandler(userService)
	authHandler := NewAuthHandler(db, sessions, userService, emailSvc, services.NewPasswordResetService(entClient))

	// Public routes
	r.Get("/login", authHandler.ServeHTTP)
	r.Post("/login", authHandler.ServeHTTP)
	r.Get("/forgot-password", authHandler.ForgotPassword)
	r.Post("/forgot-password", authHandler.ForgotPassword)
	r.Get("/reset-password", authHandler.ResetPassword)
	r.Post("/reset-password", authHandler.ResetPassword)

	setupHandler := NewSetupHandler(db, sessions, cfg)
	r.Get("/setup", setupHandler.ServeHTTP)
	r.Post("/setup", setupHandler.ServeHTTP)
	r.Get("/setup/company", settingsHandler.Show)
	r.Post("/setup/company", settingsHandler.Save)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	searchHandler := NewSearchHandler(services.NewSearchService(entClient))

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Get("/", dashboardHandler.Index)
		r.Get("/search", searchHandler.Search)
		r.Get("/schedule", scheduleHandler.Index)
		r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
			handleLogout(w, r, sessions)
		})
		r.Get("/projects", projectHandler.List)
		r.Get("/projects/new", projectHandler.Create)
		r.Post("/projects", projectHandler.Create)
		r.Get("/projects/{id}", projectHandler.Show)
		r.Get("/projects/{id}/edit", projectHandler.Update)
		r.Post("/projects/{id}", projectHandler.Update)
		r.Post("/projects/{id}/delete", projectHandler.Delete)
		r.Get("/customers", customerHandler.List)
		r.Get("/customers/new", customerHandler.Create)
		r.Post("/customers", customerHandler.Create)
		r.Get("/customers/{id}", customerHandler.Show)
		r.Get("/customers/{id}/edit", customerHandler.Update)
		r.Post("/customers/{id}", customerHandler.Update)
		r.Post("/customers/{id}/delete", customerHandler.Delete)
		r.Get("/customers/{id}/contacts", customerHandler.ListContacts)
		r.Get("/customers/{id}/contacts/options", customerHandler.Contacts)
		r.Get("/customers/{id}/contacts/new", customerHandler.NewContactForm)
		r.Post("/customers/{id}/contacts", customerHandler.CreateContact)
		r.Get("/customers/{id}/contacts/{cid}/edit", customerHandler.EditContactForm)
		r.Post("/customers/{id}/contacts/{cid}", customerHandler.UpdateContact)
		r.Post("/customers/{id}/contacts/{cid}/delete", customerHandler.DeleteContact)
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
		r.Post("/jobs/{id}/create-invoice", invoiceHandler.CreateFromJob)
		r.Post("/jobs/{id}/create-estimate", estimateHandler.CreateFromJob)
		r.Post("/jobs/{id}/subtasks/{idx}/toggle", jobHandler.ToggleSubtask)
		r.Get("/estimates", estimateHandler.List)
		r.Get("/estimates/new", estimateHandler.Create)
		r.Post("/estimates", estimateHandler.Create)
		r.Get("/estimates/{id}", estimateHandler.Show)
		r.Get("/estimates/{id}/edit", estimateHandler.Update)
		r.Post("/estimates/{id}", estimateHandler.Update)
		r.Post("/estimates/{id}/delete", estimateHandler.Delete)
		r.Post("/estimates/{id}/convert-to-invoice", estimateHandler.ConvertToInvoice)
		r.Get("/estimates/{id}/pdf", estimateHandler.PDF)
		r.Get("/invoices", invoiceHandler.List)
		r.Get("/invoices/new", invoiceHandler.Create)
		r.Post("/invoices", invoiceHandler.Create)
		r.Get("/invoices/{id}", invoiceHandler.Show)
		r.Get("/invoices/{id}/edit", invoiceHandler.Update)
		r.Post("/invoices/{id}", invoiceHandler.Update)
		r.Post("/invoices/{id}/delete", invoiceHandler.Delete)
		r.Post("/invoices/{id}/payments", invoiceHandler.RecordPayment)
		r.Get("/invoices/{id}/pdf", invoiceHandler.PDF)

		// Entity tagging
		r.Post("/jobs/{id}/tags/{tag_id}/attach", jobHandler.AttachTag)
		r.Post("/jobs/{id}/tags/{tag_id}/detach", jobHandler.DetachTag)
		r.Post("/customers/{id}/tags/{tag_id}/attach", customerHandler.AttachTag)
		r.Post("/customers/{id}/tags/{tag_id}/detach", customerHandler.DetachTag)
		r.Post("/projects/{id}/tags/{tag_id}/attach", projectHandler.AttachTag)
		r.Post("/projects/{id}/tags/{tag_id}/detach", projectHandler.DetachTag)
		r.Post("/estimates/{id}/tags/{tag_id}/attach", estimateHandler.AttachTag)
		r.Post("/estimates/{id}/tags/{tag_id}/detach", estimateHandler.DetachTag)
		r.Post("/invoices/{id}/tags/{tag_id}/attach", invoiceHandler.AttachTag)
		r.Post("/invoices/{id}/tags/{tag_id}/detach", invoiceHandler.DetachTag)

		// Entity comments (list, create, delete)
		for _, e := range []struct{ prefix, objType string }{
			{"/customers", "customer"},
			{"/jobs", "job"},
			{"/projects", "project"},
			{"/estimates", "estimate"},
			{"/invoices", "invoice"},
		} {
			r.Get(e.prefix+"/{id}/comments", commentHandler.List(e.objType))
			r.Post(e.prefix+"/{id}/comments", commentHandler.Create(e.objType))
			r.Post(e.prefix+"/{id}/comments/{cid}/delete", commentHandler.Delete(e.objType))
		}

		// Dispatcher or Admin routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.Get("/tags", tagHandler.List)
			r.Get("/tags/new", tagHandler.Create)
			r.Post("/tags", tagHandler.Create)
			r.Get("/tags/{id}/edit", tagHandler.Update)
			r.Post("/tags/{id}", tagHandler.Update)
			r.Post("/tags/{id}/delete", tagHandler.Delete)
		})

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.AdminOnly)
			r.Get("/settings", settingsHandler.Show)
			r.Post("/settings", settingsHandler.Save)
			r.Post("/settings/test-email", settingsHandler.TestEmail)
			r.Get("/settings/custom-fields", cfHandler.List)
			r.Get("/settings/custom-fields/new", cfHandler.Create)
			r.Post("/settings/custom-fields", cfHandler.Create)
			r.Get("/settings/custom-fields/{id}/edit", cfHandler.Update)
			r.Post("/settings/custom-fields/{id}", cfHandler.Update)
			r.Post("/settings/custom-fields/{id}/delete", cfHandler.Delete)
			r.Get("/users", userHandler.List)
			r.Get("/users/new", userHandler.Create)
			r.Post("/users", userHandler.Create)
			r.Get("/users/{id}", userHandler.Show)
			r.Get("/users/{id}/edit", userHandler.Update)
			r.Post("/users/{id}", userHandler.Update)
			r.Post("/users/{id}/disable", userHandler.Disable)
			r.Post("/users/{id}/reset-password", userHandler.ResetPassword)
		})
	})

	return r
}
