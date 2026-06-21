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
	activitySvc := services.NewActivityService(entClient)
	depSvc := services.NewDependencyService(entClient)
	commentHandler := NewCommentHandler(commentSvc, userService, activitySvc)
	defSvc := services.NewCustomFieldDefinitionService(entClient)
	cfHandler := NewCustomFieldHandler(defSvc, activitySvc, depSvc)
	timeEntrySvc := services.NewTimeEntryService(entClient)
	dashboardHandler := NewDashboardHandler(services.NewDashboardService(entClient), timeEntrySvc)
	// File service
	fileSvc := services.NewFileService(entClient, cfg.UploadDir, cfg.MaxUploadSize)
	fileHandler := NewFileHandler(fileSvc, activitySvc)
	activityHandler := NewActivityHandler(activitySvc, userService)

	customerHandler := NewCustomerHandler(customerService, contactSvc, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc)
	itemHandler := NewItemHandler(itemService, activitySvc)
	// Asset services
	assetTypeSvc := services.NewAssetTypeService(entClient)
	assetStatusSvc := services.NewAssetStatusService(entClient)
	assetSvc := services.NewAssetService(entClient)

	jobHandler := NewJobHandler(jobService, customerService, statusService, projectSvc, locationSvc, contactSvc, tagSvc, tagLinkSvc, defSvc, assetSvc, fileSvc, activitySvc)
	projectHandler := NewProjectHandler(projectSvc, customerService, statusService, locationSvc, jobService, tagSvc, tagLinkSvc, defSvc, activitySvc)
	scheduleHandler := NewScheduleHandler(jobService, customerService, statusService)
	invoiceService := services.NewInvoiceService(entClient)
	estimateHandler := NewEstimateHandler(services.NewEstimateService(entClient), customerService, jobService, statusService, itemService, invoiceService, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc)
	invoiceHandler := NewInvoiceHandler(invoiceService, customerService, jobService, statusService, itemService, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc)
	tagHandler := NewTagHandler(tagSvc, tagLinkSvc, activitySvc, depSvc)
	companySettingsSvc := services.NewCompanySettingsService(entClient)
	emailSvc := services.NewEmailService(companySettingsSvc)
	settingsHandler := NewSettingsHandler(companySettingsSvc, emailSvc, activitySvc)
	userHandler := NewUserHandler(userService, emailSvc, companySettingsSvc, activitySvc)
	timeEntryHandler := NewTimeEntryHandler(timeEntrySvc, userService, activitySvc)
	authHandler := NewAuthHandler(db, sessions, userService, emailSvc, services.NewPasswordResetService(entClient))
	passwordHandler := NewPasswordHandler(userService, companySettingsSvc, activitySvc)

	// Asset handlers
	assetHandler := NewAssetHandler(assetSvc, assetTypeSvc, assetStatusSvc, customerService, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc)
	assetTypeHandler := NewAssetTypeHandler(assetTypeSvc, assetStatusSvc, activitySvc, depSvc)
	assetStatusHandler := NewAssetStatusHandler(assetStatusSvc, activitySvc, depSvc)

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
		r.Use(middleware.ForcePasswordChange(userService))
		r.Get("/change-password", passwordHandler.ChangePassword)
		r.Post("/change-password", passwordHandler.ChangePassword)
		r.Get("/", dashboardHandler.Index)
		r.Get("/search", searchHandler.Search)
		r.Get("/schedule", scheduleHandler.Index)
		r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
			handleLogout(w, r, sessions)
		})
		r.Get("/projects", projectHandler.List)
		r.Get("/projects/new", projectHandler.Create)
		r.Post("/projects", projectHandler.Create)
		r.Get("/projects/activity", activityHandler.ListByType("project"))
		r.Get("/projects/{id}", projectHandler.Show)
		r.Get("/projects/{id}/edit", projectHandler.Update)
		r.Post("/projects/{id}", projectHandler.Update)
		r.Post("/projects/{id}/delete", projectHandler.Delete)
		r.Get("/customers", customerHandler.List)
		r.Get("/customers/new", customerHandler.Create)
		r.Post("/customers", customerHandler.Create)
		r.Get("/customers/activity", activityHandler.ListByType("customer"))
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
			r.Get("/activity", activityHandler.ListByType("item"))
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
		r.Get("/time-entries", timeEntryHandler.List)
		r.Post("/time-entries/clock-in", timeEntryHandler.ClockIn)
		r.Post("/time-entries/clock-out", timeEntryHandler.ClockOut)
		r.Get("/time-entries/activity", activityHandler.ListByType("time_entry"))
		r.Get("/time-entries/{id}/edit", timeEntryHandler.Update)
		r.Post("/time-entries/{id}", timeEntryHandler.Update)
		r.Get("/time-entries/{id}", timeEntryHandler.Show)
		r.Post("/time-entries/{id}/delete", timeEntryHandler.Delete)
		r.Get("/jobs", jobHandler.List)
		r.Get("/jobs/new", jobHandler.Create)
		r.Post("/jobs", jobHandler.Create)
		r.Get("/jobs/activity", activityHandler.ListByType("job"))
		r.Get("/jobs/{id}", jobHandler.Show)
		r.Get("/jobs/{id}/edit", jobHandler.Update)
		r.Post("/jobs/{id}", jobHandler.Update)
		r.Post("/jobs/{id}/delete", jobHandler.Delete)
		r.Post("/jobs/{id}/create-invoice", invoiceHandler.CreateFromJob)
		r.Post("/jobs/{id}/create-estimate", estimateHandler.CreateFromJob)
		r.Post("/jobs/{id}/subtasks/{idx}/toggle", jobHandler.ToggleSubtask)
		r.Get("/assets", assetHandler.List)
		r.Get("/assets/new", assetHandler.Create)
		r.Post("/assets", assetHandler.Create)
		r.Get("/assets/activity", activityHandler.ListByType("asset"))
		r.Get("/assets/{id}", assetHandler.Show)
		r.Get("/assets/{id}/edit", assetHandler.Update)
		r.Post("/assets/{id}", assetHandler.Update)
		r.Post("/assets/{id}/delete", assetHandler.Delete)
		r.Get("/assets/locations", assetHandler.GetLocations)
		r.Get("/estimates", estimateHandler.List)
		r.Get("/estimates/new", estimateHandler.Create)
		r.Post("/estimates", estimateHandler.Create)
		r.Get("/estimates/activity", activityHandler.ListByType("estimate"))
		r.Get("/estimates/{id}", estimateHandler.Show)
		r.Get("/estimates/{id}/edit", estimateHandler.Update)
		r.Post("/estimates/{id}", estimateHandler.Update)
		r.Post("/estimates/{id}/delete", estimateHandler.Delete)
		r.Post("/estimates/{id}/convert-to-invoice", estimateHandler.ConvertToInvoice)
		r.Get("/estimates/{id}/pdf", estimateHandler.PDF)
		r.Get("/invoices", invoiceHandler.List)
		r.Get("/invoices/new", invoiceHandler.Create)
		r.Post("/invoices", invoiceHandler.Create)
		r.Get("/invoices/activity", activityHandler.ListByType("invoice"))
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
		r.Post("/assets/{id}/tags/{tag_id}/attach", assetHandler.AttachTag)
		r.Post("/assets/{id}/tags/{tag_id}/detach", assetHandler.DetachTag)

		// Entity comments (list, create, delete)
		for _, e := range []struct{ prefix, objType string }{
			{"/customers", "customer"},
			{"/jobs", "job"},
			{"/projects", "project"},
			{"/estimates", "estimate"},
			{"/invoices", "invoice"},
			{"/assets", "asset"},
		} {
			r.Get(e.prefix+"/{id}/comments", commentHandler.List(e.objType))
			r.Post(e.prefix+"/{id}/comments", commentHandler.Create(e.objType))
			r.Post(e.prefix+"/{id}/comments/{cid}/delete", commentHandler.Delete(e.objType))
		}

		// Activity
		r.Get("/activity", activityHandler.ListAll)
		for _, e := range []struct{ prefix, objType string }{
			{"/customers", "customer"},
			{"/jobs", "job"},
			{"/projects", "project"},
			{"/estimates", "estimate"},
			{"/invoices", "invoice"},
			{"/assets", "asset"},
			{"/items", "item"},
			{"/time-entries", "time_entry"},
			{"/users", "user"},
		} {
			r.Get(e.prefix+"/{id}/activity", activityHandler.ListForObject(e.objType))
		}

		// File uploads
		r.Post("/files", fileHandler.Upload)
		r.Get("/files/{id}", fileHandler.Download)
		r.Post("/files/{id}/delete", fileHandler.Delete)

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.AdminOnly)
			// Restore archived entities
			r.Post("/customers/{id}/restore", customerHandler.Restore)
			r.Post("/jobs/{id}/restore", jobHandler.Restore)
			r.Post("/projects/{id}/restore", projectHandler.Restore)
			r.Post("/estimates/{id}/restore", estimateHandler.Restore)
			r.Post("/invoices/{id}/restore", invoiceHandler.Restore)
			r.Post("/assets/{id}/restore", assetHandler.Restore)
			r.Post("/items/{id}/restore", itemHandler.Restore)
			// Admin activity feeds
			r.Get("/settings/activity", activityHandler.ListByType("company_settings"))
			r.Get("/settings/custom-fields/activity", activityHandler.ListByType("custom_field"))
			r.Get("/settings/assets/activity", activityHandler.ListForAssetSettings)
			r.Get("/tags/activity", activityHandler.ListByType("tag"))
			r.Get("/users/activity", activityHandler.ListByType("user"))
			r.Get("/tags", tagHandler.List)
			r.Get("/tags/new", tagHandler.Create)
			r.Post("/tags", tagHandler.Create)
			r.Get("/tags/{id}/edit", tagHandler.Update)
			r.Post("/tags/{id}", tagHandler.Update)
			r.Post("/tags/{id}/delete", tagHandler.Delete)
			r.Get("/settings", settingsHandler.Show)
			r.Post("/settings", settingsHandler.Save)
			r.Post("/settings/test-email", settingsHandler.TestEmail)
			r.Get("/settings/custom-fields", cfHandler.List)
			r.Get("/settings/custom-fields/new", cfHandler.Create)
			r.Post("/settings/custom-fields", cfHandler.Create)
			r.Get("/settings/custom-fields/{id}/edit", cfHandler.Update)
			r.Post("/settings/custom-fields/{id}", cfHandler.Update)
			r.Post("/settings/custom-fields/{id}/delete", cfHandler.Delete)
			r.Get("/settings/assets", assetTypeHandler.Show)
			r.Post("/settings/asset-types", assetTypeHandler.Create)
			r.Post("/settings/asset-types/{id}", assetTypeHandler.Update)
		r.Post("/settings/asset-types/{id}/delete", assetTypeHandler.Delete)
			r.Post("/settings/asset-statuses", assetStatusHandler.Create)
			r.Post("/settings/asset-statuses/{id}", assetStatusHandler.Update)
		r.Post("/settings/asset-statuses/{id}/delete", assetStatusHandler.Delete)
			r.Get("/users", userHandler.List)
			r.Get("/users/new", userHandler.Create)
			r.Post("/users", userHandler.Create)
			r.Get("/users/{id}", userHandler.Show)
			r.Get("/users/{id}/edit", userHandler.Update)
			r.Post("/users/{id}", userHandler.Update)
			r.Post("/users/{id}/disable", userHandler.Disable)
			r.Post("/users/{id}/resend-welcome", userHandler.ResendWelcome)
			r.Post("/users/{id}/reset-password", userHandler.ResetPassword)
		})
	})

	return r
}
