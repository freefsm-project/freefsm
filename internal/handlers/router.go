package handlers

import (
	"context"
	"net/http"
	"time"

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
	policySvc := services.NewPolicyService(entClient)
	commentHandler := NewCommentHandler(commentSvc, userService, activitySvc, policySvc)
	defSvc := services.NewCustomFieldDefinitionService(entClient)
	cfHandler := NewCustomFieldHandler(defSvc, activitySvc, depSvc)
	timeEntrySvc := services.NewTimeEntryService(entClient)
	dashboardHandler := NewDashboardHandler(services.NewDashboardService(entClient), timeEntrySvc)
	// File service
	fileSvc := services.NewFileService(entClient, cfg.UploadDir, cfg.MaxUploadSize)
	fileHandler := NewFileHandler(fileSvc, activitySvc, policySvc)
	activityHandler := NewActivityHandler(activitySvc, userService, policySvc)

	customerHandler := NewCustomerHandler(customerService, contactSvc, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc, policySvc)
	itemHandler := NewItemHandler(itemService, activitySvc)
	// Asset services
	assetTypeSvc := services.NewAssetTypeService(entClient)
	assetStatusSvc := services.NewAssetStatusService(entClient)
	assetSvc := services.NewAssetService(entClient)

	jobHandler := NewJobHandler(jobService, customerService, statusService, projectSvc, locationSvc, contactSvc, tagSvc, tagLinkSvc, defSvc, assetSvc, fileSvc, activitySvc, userService, policySvc)
	projectHandler := NewProjectHandler(projectSvc, customerService, statusService, locationSvc, jobService, tagSvc, tagLinkSvc, defSvc, activitySvc, policySvc)
	scheduleHandler := NewScheduleHandler(jobService, customerService, statusService, userService, locationSvc, cfg)
	companySettingsSvc := services.NewCompanySettingsService(entClient)
	emailSvc := services.NewEmailService(companySettingsSvc)
	invoiceService := services.NewInvoiceService(entClient)
	estimateHandler := NewEstimateHandler(services.NewEstimateService(entClient), customerService, jobService, statusService, itemService, invoiceService, tagSvc, tagLinkSvc, defSvc, fileSvc, emailSvc, activitySvc)
	invoiceHandler := NewInvoiceHandler(invoiceService, customerService, jobService, statusService, itemService, tagSvc, tagLinkSvc, defSvc, fileSvc, emailSvc, activitySvc)
	tagHandler := NewTagHandler(tagSvc, tagLinkSvc, activitySvc, depSvc)
	settingsHandler := NewSettingsHandler(companySettingsSvc, emailSvc, activitySvc, cfg.UploadDir)
	userHandler := NewUserHandler(userService, emailSvc, companySettingsSvc, activitySvc, cfg)
	timeEntryHandler := NewTimeEntryHandler(timeEntrySvc, userService, activitySvc)
	authHandler := NewAuthHandler(db, sessions, userService, companySettingsSvc, emailSvc, services.NewPasswordResetService(entClient), activitySvc, cfg)
	passwordHandler := NewPasswordHandler(userService, companySettingsSvc, activitySvc)

	// Asset handlers
	assetHandler := NewAssetHandler(assetSvc, assetTypeSvc, assetStatusSvc, customerService, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc, policySvc)
	assetTypeHandler := NewAssetTypeHandler(assetTypeSvc, assetStatusSvc, activitySvc, depSvc)
	assetStatusHandler := NewAssetStatusHandler(assetStatusSvc, activitySvc, depSvc)

	// Public routes
	authPostLimiter := middleware.NewRateLimiter(5, time.Minute).Handler
	r.Get("/login", authHandler.ServeHTTP)
	r.With(authPostLimiter).Post("/login", authHandler.ServeHTTP)
	r.Get("/forgot-password", authHandler.ForgotPassword)
	r.With(authPostLimiter).Post("/forgot-password", authHandler.ForgotPassword)
	r.Get("/reset-password", authHandler.ResetPassword)
	r.With(authPostLimiter).Post("/reset-password", authHandler.ResetPassword)

	setupHandler := NewSetupHandler(db, sessions, cfg)
	r.Get("/setup", setupHandler.ServeHTTP)
	r.With(authPostLimiter).Post("/setup", setupHandler.ServeHTTP)

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
		r.Get("/settings/invoice-logo", settingsHandler.InvoiceLogo)
		r.Get("/", dashboardHandler.Index)
		r.Get("/search", searchHandler.Search)
		r.Get("/schedule", scheduleHandler.Index)
		r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
			handleLogout(w, r, sessions, activitySvc)
		})
		r.With(middleware.DispatcherOrAdmin).Get("/projects", projectHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/projects/activity", activityHandler.ListByType("project"))
		r.Get("/projects/{id}", projectHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/customers", customerHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/customers/activity", activityHandler.ListByType("customer"))
		r.Get("/customers/{id}", customerHandler.Show)
		r.Get("/customers/{id}/contacts", customerHandler.ListContacts)
		r.Get("/customers/{id}/contacts/options", customerHandler.Contacts)
		r.Route("/items", func(r chi.Router) {
			r.With(middleware.DispatcherOrAdmin).Get("/", itemHandler.List)
			r.With(middleware.DispatcherOrAdmin).Get("/activity", activityHandler.ListByType("item"))
			r.Post("/", itemHandler.Create)
			r.With(middleware.DispatcherOrAdmin).Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
				if chi.URLParam(r, "id") == "new" {
					itemHandler.Create(w, r)
					return
				}
				itemHandler.Show(w, r)
			})
			r.With(requireActiveObject(entClient, "item")).Get("/{id}/edit", itemHandler.Update)
			r.With(requireActiveObject(entClient, "item")).Post("/{id}", itemHandler.Update)
			r.With(requireActiveObject(entClient, "item")).Post("/{id}/delete", itemHandler.Delete)
		})
		r.Get("/time-entries", timeEntryHandler.List)
		r.Post("/time-entries/clock-in", timeEntryHandler.ClockIn)
		r.Post("/time-entries/clock-out", timeEntryHandler.ClockOut)
		r.With(middleware.DispatcherOrAdmin).Get("/time-entries/activity", activityHandler.ListByType("time_entry"))
		r.Get("/time-entries/{id}/edit", timeEntryHandler.Update)
		r.Post("/time-entries/{id}", timeEntryHandler.Update)
		r.Get("/time-entries/{id}", timeEntryHandler.Show)
		r.Post("/time-entries/{id}/delete", timeEntryHandler.Delete)
		r.Get("/jobs", jobHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/jobs/activity", activityHandler.ListByType("job"))
		r.Get("/jobs/{id}", jobHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/assets", assetHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/assets/activity", activityHandler.ListByType("asset"))
		r.Get("/assets/{id}", assetHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/estimates", estimateHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/estimates/activity", activityHandler.ListByType("estimate"))
		r.With(middleware.DispatcherOrAdmin).Get("/estimates/{id}", estimateHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/invoices", invoiceHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/invoices/activity", activityHandler.ListByType("invoice"))
		r.With(middleware.DispatcherOrAdmin).Get("/invoices/{id}", invoiceHandler.Show)

		// Core operational mutations
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.Get("/projects/new", projectHandler.Create)
			r.Post("/projects", projectHandler.Create)
			r.With(requireActiveObject(entClient, "project")).Get("/projects/{id}/edit", projectHandler.Update)
			r.With(requireActiveObject(entClient, "project")).Post("/projects/{id}", projectHandler.Update)
			r.With(requireActiveObject(entClient, "project")).Post("/projects/{id}/delete", projectHandler.Delete)
			r.Get("/customers/new", customerHandler.Create)
			r.Post("/customers", customerHandler.Create)
			r.With(requireActiveObject(entClient, "customer")).Get("/customers/{id}/edit", customerHandler.Update)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}", customerHandler.Update)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}/delete", customerHandler.Delete)
			r.With(requireActiveObject(entClient, "customer")).Get("/customers/{id}/contacts/new", customerHandler.NewContactForm)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}/contacts", customerHandler.CreateContact)
			r.With(requireActiveObject(entClient, "customer")).Get("/customers/{id}/contacts/{cid}/edit", customerHandler.EditContactForm)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}/contacts/{cid}", customerHandler.UpdateContact)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}/contacts/{cid}/delete", customerHandler.DeleteContact)
			r.Get("/jobs/new", jobHandler.Create)
			r.Post("/jobs", jobHandler.Create)
			r.With(requireActiveObject(entClient, "job")).Get("/jobs/{id}/edit", jobHandler.Update)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}", jobHandler.Update)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}/delete", jobHandler.Delete)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}/subtasks/{idx}/toggle", jobHandler.ToggleSubtask)
			r.Post("/schedule/dispatch", scheduleHandler.DispatchUpdate)
			r.Post("/schedule/move", scheduleHandler.CalendarMove)
			r.Post("/schedule/calendar/move", scheduleHandler.CalendarMove)
			r.Get("/assets/new", assetHandler.Create)
			r.Post("/assets", assetHandler.Create)
			r.With(requireActiveObject(entClient, "asset")).Get("/assets/{id}/edit", assetHandler.Update)
			r.With(requireActiveObject(entClient, "asset")).Post("/assets/{id}", assetHandler.Update)
			r.With(requireActiveObject(entClient, "asset")).Post("/assets/{id}/delete", assetHandler.Delete)
			r.Get("/assets/locations", assetHandler.GetLocations)
		})

		// Billing and document mutations
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}/create-invoice", invoiceHandler.CreateFromJob)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}/create-estimate", estimateHandler.CreateFromJob)
			r.Get("/estimates/new", estimateHandler.Create)
			r.Post("/estimates", estimateHandler.Create)
			r.With(requireActiveObject(entClient, "estimate")).Get("/estimates/{id}/edit", estimateHandler.Update)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}", estimateHandler.Update)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}/delete", estimateHandler.Delete)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}/convert-to-invoice", estimateHandler.ConvertToInvoice)
			r.Get("/estimates/{id}/pdf", estimateHandler.PDF)
			r.Get("/estimates/{id}/pdf/preview", estimateHandler.PreviewPDF)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}/pdf/save", estimateHandler.SavePDF)
			r.With(requireActiveObject(entClient, "estimate")).Get("/estimates/{id}/email", estimateHandler.Email)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}/email", estimateHandler.Email)
			r.Get("/invoices/new", invoiceHandler.Create)
			r.Post("/invoices", invoiceHandler.Create)
			r.With(requireActiveObject(entClient, "invoice")).Get("/invoices/{id}/edit", invoiceHandler.Update)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}", invoiceHandler.Update)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}/delete", invoiceHandler.Delete)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}/payments", invoiceHandler.RecordPayment)
			r.Get("/invoices/{id}/pdf", invoiceHandler.PDF)
			r.Get("/invoices/{id}/pdf/preview", invoiceHandler.PreviewPDF)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}/pdf/save", invoiceHandler.SavePDF)
			r.With(requireActiveObject(entClient, "invoice")).Get("/invoices/{id}/email", invoiceHandler.Email)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}/email", invoiceHandler.Email)
		})

		// Entity tagging
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}/tags/{tag_id}/attach", jobHandler.AttachTag)
			r.With(requireActiveObject(entClient, "job")).Post("/jobs/{id}/tags/{tag_id}/detach", jobHandler.DetachTag)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}/tags/{tag_id}/attach", customerHandler.AttachTag)
			r.With(requireActiveObject(entClient, "customer")).Post("/customers/{id}/tags/{tag_id}/detach", customerHandler.DetachTag)
			r.With(requireActiveObject(entClient, "project")).Post("/projects/{id}/tags/{tag_id}/attach", projectHandler.AttachTag)
			r.With(requireActiveObject(entClient, "project")).Post("/projects/{id}/tags/{tag_id}/detach", projectHandler.DetachTag)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}/tags/{tag_id}/attach", estimateHandler.AttachTag)
			r.With(requireActiveObject(entClient, "estimate")).Post("/estimates/{id}/tags/{tag_id}/detach", estimateHandler.DetachTag)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}/tags/{tag_id}/attach", invoiceHandler.AttachTag)
			r.With(requireActiveObject(entClient, "invoice")).Post("/invoices/{id}/tags/{tag_id}/detach", invoiceHandler.DetachTag)
			r.With(requireActiveObject(entClient, "asset")).Post("/assets/{id}/tags/{tag_id}/attach", assetHandler.AttachTag)
			r.With(requireActiveObject(entClient, "asset")).Post("/assets/{id}/tags/{tag_id}/detach", assetHandler.DetachTag)
		})

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
			r.With(requireActiveObject(entClient, e.objType)).Post(e.prefix+"/{id}/comments", commentHandler.Create(e.objType))
			r.With(requireActiveObject(entClient, e.objType)).Post(e.prefix+"/{id}/comments/{cid}/delete", commentHandler.Delete(e.objType))
		}

		// Activity
		r.With(middleware.DispatcherOrAdmin).Get("/activity", activityHandler.ListAll)
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
			r.Get("/setup/company", settingsHandler.Show)
			r.Post("/setup/company", settingsHandler.Save)
			r.Post("/settings/invoice-logo", settingsHandler.UploadInvoiceLogo)
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
