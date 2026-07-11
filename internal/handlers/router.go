package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
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
		info := &middleware.UserInfo{
			ID:                 u.ID,
			Name:               u.Name,
			Email:              u.Email,
			Role:               u.Role,
			FontSize:           u.FontSize,
			LastScheduleTab:    u.LastScheduleTab,
			LastSchedulePeriod: u.LastSchedulePeriod,
		}
		if u.CompanyID != nil {
			info.CompanyID = *u.CompanyID
		}
		return info, nil
	}
	authMW := middleware.Auth(sessions, userFn)

	objects := objectref.NewEntDirectory(entClient)
	activeObjectGuard := func(objectType objectref.Type) func(http.Handler) http.Handler {
		return requireActiveObject(objects, objectType)
	}
	customerService := services.NewCustomerService(entClient)
	statusService := services.NewStatusService(entClient)
	jobService := services.NewJobService(entClient)
	itemService := services.NewItemService(entClient)
	contactSvc := services.NewCustomerContactService(entClient)
	projectSvc := services.NewProjectService(entClient)
	locationSvc := services.NewLocationService(entClient)
	tagSvc := services.NewTagService(entClient)
	tagLinkSvc := services.NewTagLinkService(entClient, objects)
	commentSvc := services.NewCommentService(entClient, objects)
	activitySvc := services.NewActivityService(entClient, objects)
	depSvc := services.NewDependencyService(entClient)
	policySvc := services.NewPolicyService(entClient, objects)
	estimateService := services.NewEstimateService(entClient)
	invoiceService := services.NewInvoiceService(entClient)
	settlementService := settlement.New(db)
	commentHandler := NewCommentHandler(commentSvc, userService, activitySvc, policySvc, objects)
	defSvc := services.NewCustomFieldDefinitionService(entClient)
	cfHandler := NewCustomFieldHandler(defSvc, activitySvc, depSvc)
	timeEntrySvc := services.NewTimeEntryService(entClient)
	dashboardHandler := NewDashboardHandler(services.NewDashboardService(entClient, db), timeEntrySvc)
	// File service
	fileSvc := services.NewFileService(entClient, objects, cfg.UploadDir, cfg.MaxUploadSize)
	fileHandler := NewFileHandler(fileSvc, activitySvc, policySvc, objects)
	activityHandler := NewActivityHandler(activitySvc, userService, policySvc, objects)

	customerHandler := NewCustomerHandler(customerService, contactSvc, locationSvc, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc, policySvc, jobService, estimateService, invoiceService, statusService, settlementService)
	itemHandler := NewItemHandler(itemService, activitySvc)
	// Asset services
	assetTypeSvc := services.NewAssetTypeService(entClient)
	assetStatusSvc := services.NewAssetStatusService(entClient)
	assetSvc := services.NewAssetService(entClient)

	jobHandler := NewJobHandler(jobService, customerService, statusService, projectSvc, locationSvc, contactSvc, tagSvc, tagLinkSvc, defSvc, assetSvc, assetTypeSvc, assetStatusSvc, fileSvc, activitySvc, userService, policySvc, timeEntrySvc)
	projectHandler := NewProjectHandler(projectSvc, customerService, statusService, locationSvc, jobService, tagSvc, tagLinkSvc, defSvc, activitySvc, policySvc)
	scheduleHandler := NewScheduleHandler(jobService, customerService, statusService, userService, locationSvc, invoiceService, activitySvc, cfg)
	companySettingsSvc := services.NewCompanySettingsService(entClient)
	emailSvc := services.NewEmailService(companySettingsSvc)
	inviteSvc := services.NewInvitationService(entClient)
	estimateHandler := NewEstimateHandler(estimateService, customerService, jobService, statusService, itemService, invoiceService, tagSvc, tagLinkSvc, defSvc, fileSvc, emailSvc, activitySvc, policySvc)
	invoiceHandler := NewInvoiceHandler(invoiceService, customerService, jobService, assetSvc, statusService, itemService, tagSvc, tagLinkSvc, defSvc, fileSvc, emailSvc, activitySvc, policySvc, settlementService)
	tagHandler := NewTagHandler(tagSvc, tagLinkSvc, activitySvc, depSvc)
	settingsHandler := NewSettingsHandler(companySettingsSvc, emailSvc, activitySvc, cfg.UploadDir)
	jobStatusHandler := NewJobStatusHandler(statusService, activitySvc)
	userHandler := NewUserHandler(userService, emailSvc, inviteSvc, companySettingsSvc, activitySvc, cfg)
	timeEntryHandler := NewTimeEntryHandler(timeEntrySvc, userService, jobService, activitySvc)
	authHandler := NewAuthHandler(db, sessions, userService, companySettingsSvc, emailSvc, services.NewPasswordResetService(entClient), inviteSvc, activitySvc, cfg)
	passwordHandler := NewPasswordHandler(userService, companySettingsSvc, activitySvc)

	// Asset handlers
	assetHandler := NewAssetHandler(assetSvc, assetTypeSvc, assetStatusSvc, customerService, locationSvc, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc, policySvc)
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
	r.Get("/accept-invite", authHandler.AcceptInvite)
	r.With(authPostLimiter).Post("/accept-invite", authHandler.AcceptInvite)

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
		r.Get("/preferences", userHandler.Preferences)
		r.Post("/preferences", userHandler.Preferences)
		r.Get("/settings/invoice-logo", settingsHandler.InvoiceLogo)
		r.Get("/", dashboardHandler.Index)
		r.Get("/dashboard/widgets/new", dashboardHandler.NewWidget)
		r.Post("/dashboard/widgets/add", dashboardHandler.AddWidget)
		r.Post("/dashboard/widgets/{id}/remove", dashboardHandler.RemoveWidget)
		r.Post("/dashboard/widgets/reorder", dashboardHandler.ReorderWidget)
		r.Post("/dashboard/widgets/reset", dashboardHandler.ResetWidgets)
		r.Post("/dashboard/widgets/company-default", dashboardHandler.SaveCompanyDefaultWidgets)
		r.Get("/search", searchHandler.Search)
		r.Get("/schedule", scheduleHandler.Index)
		r.Get("/schedule/activity", activityHandler.ListSchedule)
		r.Post("/logout", func(w http.ResponseWriter, r *http.Request) {
			handleLogout(w, r, sessions, activitySvc)
		})
		r.With(middleware.DispatcherOrAdmin).Get("/projects", projectHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/projects/activity", activityHandler.ListByType(objectref.TypeProject))
		r.Get("/projects/{id}", projectHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/customers", customerHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/customers/activity", activityHandler.ListByType(objectref.TypeCustomer))
		r.Get("/customers/{id}", customerHandler.Show)
		r.Get("/customers/{id}/contacts", customerHandler.ListContacts)
		r.Get("/customers/{id}/contacts/options", customerHandler.Contacts)
		r.Get("/customers/{id}/locations", customerHandler.ListLocations)
		r.Get("/customers/{id}/locations/options", customerHandler.LocationOptions)
		r.With(middleware.DispatcherOrAdmin).Get("/customers/{id}/projects/options", projectHandler.ProjectOptions)
		r.With(middleware.DispatcherOrAdmin).Get("/customers/{id}/assets/options", jobHandler.AssetOptions)
		r.Route("/items", func(r chi.Router) {
			r.With(middleware.DispatcherOrAdmin).Get("/", itemHandler.List)
			r.With(middleware.DispatcherOrAdmin).Get("/activity", activityHandler.ListByType(objectref.TypeItem))
			r.With(middleware.DispatcherOrAdmin).Post("/inline", itemHandler.CreateInline)
			r.Post("/", itemHandler.Create)
			r.With(middleware.DispatcherOrAdmin).Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
				if chi.URLParam(r, "id") == "new" {
					itemHandler.Create(w, r)
					return
				}
				itemHandler.Show(w, r)
			})
			r.With(activeObjectGuard(objectref.TypeItem)).Get("/{id}/edit", itemHandler.Update)
			r.With(activeObjectGuard(objectref.TypeItem)).Post("/{id}", itemHandler.Update)
			r.With(activeObjectGuard(objectref.TypeItem)).Post("/{id}/delete", itemHandler.Delete)
		})
		r.Get("/time-entries", timeEntryHandler.List)
		r.Post("/time-entries/clock-in", timeEntryHandler.ClockIn)
		r.Post("/time-entries/clock-out", timeEntryHandler.ClockOut)
		r.With(middleware.DispatcherOrAdmin).Get("/time-entries/activity", activityHandler.ListByType(objectref.TypeTimeEntry))
		r.Get("/time-entries/{id}/edit", timeEntryHandler.Update)
		r.Post("/time-entries/{id}", timeEntryHandler.Update)
		r.Get("/time-entries/{id}", timeEntryHandler.Show)
		r.Post("/time-entries/{id}/delete", timeEntryHandler.Delete)
		r.Get("/jobs", jobHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/jobs/activity", activityHandler.ListByType(objectref.TypeJob))
		r.Post("/jobs/{id}/status", jobHandler.UpdateStatus)
		r.Post("/jobs/{id}/clock-in", jobHandler.ClockIn)
		r.Post("/jobs/{id}/clock-out", jobHandler.ClockOut)
		r.Get("/jobs/{id}", jobHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/assets", assetHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/assets/activity", activityHandler.ListByType(objectref.TypeAsset))
		r.Get("/assets/{id}", assetHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/estimates", estimateHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/estimates/activity", activityHandler.ListByType(objectref.TypeEstimate))
		r.With(middleware.DispatcherOrAdmin).Get("/estimates/{id}", estimateHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/invoices", invoiceHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/invoices/activity", activityHandler.ListByType(objectref.TypeInvoice))
		r.With(middleware.DispatcherOrAdmin).Get("/invoices/{id}", invoiceHandler.Show)

		// Core operational mutations
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.Get("/projects/new", projectHandler.Create)
			r.Post("/projects", projectHandler.Create)
			r.Post("/projects/inline", projectHandler.CreateInline)
			r.With(activeObjectGuard(objectref.TypeProject)).Get("/projects/{id}/edit", projectHandler.Update)
			r.With(activeObjectGuard(objectref.TypeProject)).Post("/projects/{id}", projectHandler.Update)
			r.With(activeObjectGuard(objectref.TypeProject)).Post("/projects/{id}/delete", projectHandler.Delete)
			r.Get("/customers/new", customerHandler.Create)
			r.Post("/customers", customerHandler.Create)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/edit", customerHandler.Update)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}", customerHandler.Update)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/delete", customerHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/credit/refunds", customerHandler.RefundCredit)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/credit/refunds/{refund_id}/reverse", customerHandler.ReverseRefund)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/contacts/new", customerHandler.NewContactForm)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/contacts", customerHandler.CreateContact)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/contacts/inline", customerHandler.CreateContactInline)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/contacts/{cid}/edit", customerHandler.EditContactForm)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/contacts/{cid}", customerHandler.UpdateContact)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/contacts/{cid}/delete", customerHandler.DeleteContact)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/locations/new", customerHandler.NewLocationForm)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/locations", customerHandler.CreateLocation)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/locations/inline", customerHandler.CreateLocationInline)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/locations/{lid}/edit", customerHandler.EditLocationForm)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/locations/{lid}", customerHandler.UpdateLocation)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/locations/{lid}/delete", customerHandler.DeleteLocation)
			r.Get("/jobs/new", jobHandler.Create)
			r.Post("/jobs", jobHandler.Create)
			r.With(activeObjectGuard(objectref.TypeJob)).Get("/jobs/{id}/edit", jobHandler.Update)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}", jobHandler.Update)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/create-next-occurrence", jobHandler.CreateNextOccurrence)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/cancel-next-occurrence", jobHandler.CancelNextOccurrence)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/delete", jobHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/subtasks/{idx}/toggle", jobHandler.ToggleSubtask)
			r.Post("/schedule/dispatch", scheduleHandler.DispatchUpdate)
			r.Post("/schedule/move", scheduleHandler.CalendarMove)
			r.Post("/schedule/calendar/move", scheduleHandler.CalendarMove)
			r.Get("/assets/new", assetHandler.Create)
			r.Post("/assets", assetHandler.Create)
			r.Post("/assets/inline", assetHandler.CreateInline)
			r.With(activeObjectGuard(objectref.TypeAsset)).Get("/assets/{id}/edit", assetHandler.Update)
			r.With(activeObjectGuard(objectref.TypeAsset)).Post("/assets/{id}", assetHandler.Update)
			r.With(activeObjectGuard(objectref.TypeAsset)).Post("/assets/{id}/delete", assetHandler.Delete)
			r.Get("/assets/locations", assetHandler.GetLocations)
		})

		// Billing and document mutations
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/create-invoice", invoiceHandler.CreateFromJob)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/create-estimate", estimateHandler.CreateFromJob)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/create-invoice", invoiceHandler.CreateFromCustomer)
			r.Get("/estimates/new", estimateHandler.Create)
			r.Post("/estimates", estimateHandler.Create)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Get("/estimates/{id}/edit", estimateHandler.Update)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}", estimateHandler.Update)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/delete", estimateHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/convert-to-invoice", estimateHandler.ConvertToInvoice)
			r.Get("/estimates/{id}/pdf", estimateHandler.PDF)
			r.Get("/estimates/{id}/pdf/preview", estimateHandler.PreviewPDF)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/pdf/save", estimateHandler.SavePDF)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Get("/estimates/{id}/email", estimateHandler.Email)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/email", estimateHandler.Email)
			r.Get("/invoices/new", invoiceHandler.Create)
			r.Post("/invoices", invoiceHandler.Create)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Get("/invoices/{id}/edit", invoiceHandler.Update)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}", invoiceHandler.Update)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/delete", invoiceHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/finalize", invoiceHandler.Finalize)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/payments", invoiceHandler.RecordPayment)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/payments/{payment_id}/reverse", invoiceHandler.ReversePayment)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/credit-applications", invoiceHandler.ApplyCredit)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/credit-applications/{application_id}/reverse", invoiceHandler.ReverseCreditApplication)
			r.Get("/invoices/{id}/pdf", invoiceHandler.PDF)
			r.Get("/invoices/{id}/pdf/preview", invoiceHandler.PreviewPDF)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/pdf/save", invoiceHandler.SavePDF)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Get("/invoices/{id}/email", invoiceHandler.Email)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/email", invoiceHandler.Email)
		})

		// Entity tagging
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/tags/{tag_id}/attach", jobHandler.AttachTag)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/tags/{tag_id}/detach", jobHandler.DetachTag)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/tags/{tag_id}/attach", customerHandler.AttachTag)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/tags/{tag_id}/detach", customerHandler.DetachTag)
			r.With(activeObjectGuard(objectref.TypeProject)).Post("/projects/{id}/tags/{tag_id}/attach", projectHandler.AttachTag)
			r.With(activeObjectGuard(objectref.TypeProject)).Post("/projects/{id}/tags/{tag_id}/detach", projectHandler.DetachTag)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/tags/{tag_id}/attach", estimateHandler.AttachTag)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/tags/{tag_id}/detach", estimateHandler.DetachTag)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/tags/{tag_id}/attach", invoiceHandler.AttachTag)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/tags/{tag_id}/detach", invoiceHandler.DetachTag)
			r.With(activeObjectGuard(objectref.TypeAsset)).Post("/assets/{id}/tags/{tag_id}/attach", assetHandler.AttachTag)
			r.With(activeObjectGuard(objectref.TypeAsset)).Post("/assets/{id}/tags/{tag_id}/detach", assetHandler.DetachTag)
		})

		// Entity comments (list, create, delete)
		for _, e := range []struct {
			prefix  string
			objType objectref.Type
		}{
			{"/customers", objectref.TypeCustomer},
			{"/jobs", objectref.TypeJob},
			{"/projects", objectref.TypeProject},
			{"/estimates", objectref.TypeEstimate},
			{"/invoices", objectref.TypeInvoice},
			{"/assets", objectref.TypeAsset},
		} {
			r.Get(e.prefix+"/{id}/comments", commentHandler.List(e.objType))
			r.With(activeObjectGuard(e.objType)).Post(e.prefix+"/{id}/comments", commentHandler.Create(e.objType))
			r.With(activeObjectGuard(e.objType)).Post(e.prefix+"/{id}/comments/{cid}/delete", commentHandler.Delete(e.objType))
		}

		// Activity
		r.With(middleware.DispatcherOrAdmin).Get("/activity", activityHandler.ListAll)
		for _, e := range []struct {
			prefix  string
			objType objectref.Type
		}{
			{"/customers", objectref.TypeCustomer},
			{"/jobs", objectref.TypeJob},
			{"/projects", objectref.TypeProject},
			{"/estimates", objectref.TypeEstimate},
			{"/invoices", objectref.TypeInvoice},
			{"/assets", objectref.TypeAsset},
			{"/items", objectref.TypeItem},
			{"/time-entries", objectref.TypeTimeEntry},
			{"/users", objectref.TypeUser},
		} {
			r.Get(e.prefix+"/{id}/activity", activityHandler.ListForObject(e.objType))
		}

		// File uploads
		r.Post("/files", fileHandler.Upload)
		r.Get("/files/{id}", fileHandler.Download)
		r.Post("/files/{id}/rename", fileHandler.Rename)
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
			r.Get("/settings/activity", activityHandler.ListByType(objectref.TypeCompanySettings))
			r.Get("/settings/custom-fields/activity", activityHandler.ListByType(objectref.TypeCustomField))
			r.Get("/settings/assets/activity", activityHandler.ListForAssetSettings)
			r.Get("/settings/job-statuses/activity", activityHandler.ListByType(objectref.TypeJobStatus))
			r.Get("/tags/activity", activityHandler.ListByType(objectref.TypeTag))
			r.Get("/users/activity", activityHandler.ListByType(objectref.TypeUser))
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
			r.Get("/settings/job-statuses", jobStatusHandler.List)
			r.Post("/settings/job-statuses", jobStatusHandler.Create)
			r.Post("/settings/job-statuses/{id}", jobStatusHandler.Update)
			r.Post("/settings/job-statuses/{id}/delete", jobStatusHandler.Delete)
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
