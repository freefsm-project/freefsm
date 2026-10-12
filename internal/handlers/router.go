package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func New(db *pgxpool.Pool, entClient *ent.Client, sessions *services.SessionService, cfg *config.Config, controls ...*instancecontrol.Control) http.Handler {
	r := chi.NewRouter()
	authority := authorization.New(db)
	authorization.InstallCustomerBoundary(entClient)
	authorization.InstallItemBoundary(entClient)
	authorityHandler := authorizationHandler{svc: authority}
	var control *instancecontrol.Control
	if len(controls) > 0 {
		control = controls[0]
	}

	userService := services.NewUserService(entClient)
	userFn := func(ctx context.Context, userID int64) (*middleware.UserInfo, error) {
		u, err := userService.GetByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		if !u.IsActive {
			return nil, fmt.Errorf("disabled user")
		}
		principal, authErr := authority.Administrator(ctx, authorization.Actor{UserID: userID})
		info := &middleware.UserInfo{
			ConfigurationView:      principal.Allows("configuration.company.view", authorization.Configuration) || principal.Allows("configuration.documents.view", authorization.Configuration),
			MailSettingsView:       principal.Allows("configuration.mail.view", authorization.Configuration),
			MapSettingsView:        principal.Allows("configuration.maps.view", authorization.Configuration),
			ID:                     u.ID,
			Name:                   u.Name,
			Email:                  u.Email,
			Role:                   u.Role,
			AuthorizationLoaded:    true,
			AuthorizationReady:     principal.Ready,
			ProtectedAdministrator: authErr == nil && principal.Protected && principal.Ready,
			CustomerView:           principal.Allows("customer.view", authorization.All),
			DirectoryView:          principal.Allows("directory.view", authorization.All),
			CustomerCreate:         principal.Allows("customer.create", authorization.All),
			CustomerEdit:           principal.Allows("customer.edit", authorization.All),
			CustomerArchive:        principal.Allows("customer.archive", authorization.All),
			CustomerFinancial:      principal.Allows("customer.financial.view", authorization.All),
			CustomerContext:        principal.Allows("customer.view", authorization.Context),
			CanReviewAuthorization: authErr == nil && principal.Protected,
			FontSize:               u.FontSize,
			LastScheduleTab:        u.LastScheduleTab,
			LastSchedulePeriod:     u.LastSchedulePeriod,
		}
		if u.CompanyID != nil {
			info.CompanyID = *u.CompanyID
		}
		info.ItemPermissions = make(map[string]bool)
		for _, permission := range []string{"item.view", "item.create", "item.edit", "item.archive", "item.restore", "pricebook.view", "pricebook.create", "pricebook.edit", "cost.view", "cost.edit"} {
			info.ItemPermissions[permission] = principal.Allows(permission, authorization.All)
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
	activitySvc := services.NewActivityService(entClient, objects)
	activityResolver := services.NewActivityResolver(entClient)
	depSvc := services.NewDependencyService(entClient)
	policySvc := services.NewPolicyService(entClient, objects)
	estimateService := services.NewEstimateService(entClient)
	invoiceService := services.NewInvoiceService(entClient, db)
	settlementService := settlement.New(db)
	conversionService := conversion.New(db)
	statusflowService := statusflow.New(db)
	deliveryService := delivery.New(db, cfg.PublicURL)
	deliveryService.SetInstanceControl(control)
	commentHandler := NewCommentHandler(objects)
	defSvc := services.NewCustomFieldDefinitionService(entClient)
	cfHandler := NewCustomFieldHandler(defSvc)
	timeEntrySvc := services.NewTimeEntryService(entClient)
	dashboardHandler := NewDashboardHandler(services.NewDashboardService(entClient, db), timeEntrySvc)
	// File service
	fileSvc := services.NewFileService(entClient, objects, cfg.UploadDir, cfg.MaxUploadSize)
	fileHandler := NewFileHandler(fileSvc, activitySvc, policySvc, objects)
	activityHandler := NewActivityHandler(activitySvc, activityResolver, policySvc, conversionService)

	customerHandler := NewCustomerHandler(customerService, contactSvc, locationSvc, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc, policySvc, jobService, estimateService, invoiceService, statusService, settlementService)
	customerHandler.statusflow = statusflowService
	itemHandler := NewItemHandler(itemService)
	// Asset services
	assetTypeSvc := services.NewAssetTypeService(entClient)
	assetStatusSvc := services.NewAssetStatusService(entClient)
	assetSvc := services.NewAssetService(entClient)

	jobHandler := NewJobHandler(jobService, customerService, statusService, projectSvc, locationSvc, contactSvc, tagSvc, tagLinkSvc, defSvc, assetSvc, assetTypeSvc, assetStatusSvc, fileSvc, activitySvc, userService, policySvc, timeEntrySvc)
	jobHandler.statusflow = statusflowService
	projectHandler := NewProjectHandler(projectSvc, customerService, statusService, locationSvc, jobService, tagSvc, tagLinkSvc, defSvc, activitySvc, policySvc)
	projectHandler.fileSvc = fileSvc
	projectHandler.statusflow = statusflowService
	scheduleHandler := NewScheduleHandler(jobService, customerService, statusService, userService, locationSvc, invoiceService, activitySvc, cfg)
	companySettingsSvc := services.NewCompanySettingsService(entClient)
	emailSvc := services.NewEmailService(companySettingsSvc)
	emailSvc.SetInstanceControl(control)
	inviteSvc := services.NewInvitationService(entClient)
	estimateHandler := NewEstimateHandler(estimateService, customerService, jobService, statusService, itemService, invoiceService, tagSvc, tagLinkSvc, defSvc, fileSvc, deliveryService, activitySvc, policySvc, conversionService)
	invoiceHandler := NewInvoiceHandler(invoiceService, customerService, jobService, assetSvc, statusService, itemService, tagSvc, tagLinkSvc, defSvc, fileSvc, deliveryService, activitySvc, policySvc, settlementService, conversionService)
	estimateHandler.statusflow = statusflowService
	invoiceHandler.statusflow = statusflowService
	tagHandler := NewTagHandler(tagSvc, tagLinkSvc, activitySvc, depSvc)
	settingsHandler := NewSettingsHandler(companySettingsSvc, emailSvc, activitySvc, cfg.UploadDir)
	settingsHandler.control = control
	settingsHandler.authority = authority
	settingsHandler.users = userService
	integrations := &integrationHandler{authority: authority, email: emailSvc, control: control, geocoderDefault: cfg.GeocoderURL}
	statusSettingsHandler := NewStatusSettingsHandler(statusflowService)
	userHandler := NewUserHandler(userService, emailSvc, inviteSvc, companySettingsSvc, activitySvc, cfg)
	timeEntryHandler := NewTimeEntryHandler(timeEntrySvc, userService, jobService, activitySvc)
	authHandler := NewAuthHandler(db, sessions, userService, companySettingsSvc, emailSvc, services.NewPasswordResetService(entClient), inviteSvc, activitySvc, cfg)
	userHandler.resetSvc = services.NewPasswordResetService(entClient)
	passwordHandler := NewPasswordHandler(userService, companySettingsSvc, activitySvc)

	// Asset handlers
	assetHandler := NewAssetHandler(assetSvc, assetTypeSvc, assetStatusSvc, customerService, locationSvc, tagSvc, tagLinkSvc, defSvc, fileSvc, activitySvc, policySvc)
	assetTypeHandler := NewAssetTypeHandler(assetTypeSvc, assetStatusSvc, activitySvc, depSvc)

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

	setupHandler := NewSetupHandler(db, sessions, userService, companySettingsSvc, cfg)
	r.Get("/setup", setupHandler.ServeHTTP)
	r.With(authPostLimiter).Post("/setup", setupHandler.ServeHTTP)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	searchHandler := NewSearchHandler(services.NewSearchService(entClient, db))

	// Authenticated routes
	r.Group(func(r chi.Router) {
		r.Use(authMW)
		r.Use(presentationContext(authority))
		r.Use(middleware.ForcePasswordChange(userService))
		r.Use(bindAuthorization(authority))
		r.Get("/change-password", passwordHandler.ChangePassword)
		r.Post("/change-password", passwordHandler.ChangePassword)
		r.Get("/preferences", presentationPreferences(authority))
		r.Post("/preferences", presentationPreferences(authority))
		r.Get("/web-actions/{operation}", authorityHandler.WebReceipt)
		r.Get("/settings/invoice-logo", settingsHandler.InvoiceLogo)
		r.Get("/settings", settingsHandler.Show)
		r.Post("/settings", settingsHandler.Save)
		r.Post("/settings/numbering-review", settingsHandler.ReviewNumber)
		r.Post("/settings/invoice-logo", settingsHandler.UploadInvoiceLogo)
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
		r.Get("/customers", customerHandler.List)
		r.Get("/customers/activity", activityHandler.ListByType(objectref.TypeCustomer))
		r.Get("/customers/new", customerHandler.Create)
		r.Post("/customers", customerHandler.Create)
		r.Get("/customers/{id}/edit", customerHandler.Update)
		r.Post("/customers/{id}", customerHandler.Update)
		r.Get("/customers/{id}/document-defaults", settingsHandler.CustomerDefaults)
		r.Post("/customers/{id}/document-defaults", settingsHandler.CustomerDefaults)
		r.Post("/customers/{id}/delete", customerHandler.Delete)
		r.Post("/customers/{id}/restore", customerHandler.Restore)
		r.Get("/jobs/{id}/customer-context", authorityHandler.Context)
		r.Get("/jobs/{id}/master-context", authorityHandler.MasterContext)
		r.Get("/settings/authorization", authorityHandler.Show)
		r.Get("/directory", authorityHandler.Directory)
		r.Get("/settings/mail", integrations.settings)
		r.Get("/locations/{id}/coordinates", integrations.coordinates)
		r.Post("/locations/{id}/coordinates", integrations.coordinates)
		r.Post("/locations/{id}/coordinates/lookup", integrations.coordinates)
		r.Post("/settings/mail", integrations.settings)
		r.Get("/settings/maps", integrations.settings)
		r.Post("/settings/maps", integrations.settings)
		r.Get("/settings/open-tracking", integrations.settings)
		r.Post("/settings/open-tracking", integrations.settings)
		r.Post("/settings/test-email", integrations.testMail)
		r.Post("/settings/authorization/roles", authorityHandler.SaveRole)
		r.Post("/settings/authorization/roles/remove", authorityHandler.RemoveRole)
		r.Post("/settings/authorization/assign", authorityHandler.Assign)
		r.Post("/settings/authorization/cutover", authorityHandler.Cutover)
		r.Get("/customers/{id}", customerHandler.Show)
		registerCustomerChildren(r)
		r.With(middleware.DispatcherOrAdmin).Get("/customers/{id}/projects/options", projectHandler.ProjectOptions)
		r.With(middleware.DispatcherOrAdmin).Get("/customers/{id}/assets/options", jobHandler.AssetOptions)
		r.Route("/items", func(r chi.Router) {
			r.Get("/", itemHandler.List)
			r.With(middleware.DispatcherOrAdmin).Get("/activity", activityHandler.ListByType(objectref.TypeItem))
			r.Post("/inline", itemHandler.CreateInline)
			r.Post("/", itemHandler.Create)
			r.Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
				if chi.URLParam(r, "id") == "new" {
					itemHandler.Create(w, r)
					return
				}
				itemHandler.Show(w, r)
			})
			r.With(activeObjectGuard(objectref.TypeItem)).Get("/{id}/edit", itemHandler.Update)
			r.With(activeObjectGuard(objectref.TypeItem)).Post("/{id}", itemHandler.Update)
			r.With(activeObjectGuard(objectref.TypeItem)).Post("/{id}/delete", itemHandler.Delete)
			r.Post("/{id}/restore", itemHandler.Restore)
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
		r.Post("/projects/{id}/status", transitionStatus(statusflowService, statusflow.Project))
		r.Post("/estimates/{id}/status", transitionStatus(statusflowService, statusflow.Estimate))
		r.Post("/invoices/{id}/status", transitionStatus(statusflowService, statusflow.Invoice))
		r.Post("/jobs/{id}/clock-in", jobHandler.ClockIn)
		r.Post("/jobs/{id}/clock-out", jobHandler.ClockOut)
		r.Get("/jobs/{id}", jobHandler.Show)
		for _, parent := range customfields.ExistingParents() {
			typ := parent.Type
			r.Get("/"+typ+"s/{id}/custom-fields", cfHandler.Values(typ))
			r.Post("/"+typ+"s/{id}/custom-fields", cfHandler.Values(typ))
		}
		r.With(middleware.DispatcherOrAdmin).Get("/assets", assetHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/assets/activity", activityHandler.ListByType(objectref.TypeAsset))
		r.Get("/assets/{id}", assetHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/estimates", estimateHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/estimates/activity", activityHandler.ListByType(objectref.TypeEstimate))
		r.Get("/estimates/{id}", estimateHandler.Show)
		r.With(middleware.DispatcherOrAdmin).Get("/invoices", invoiceHandler.List)
		r.With(middleware.DispatcherOrAdmin).Get("/invoices/activity", activityHandler.ListByType(objectref.TypeInvoice))
		r.Get("/invoices/{id}", invoiceHandler.Show)

		// Core operational mutations
		r.With(detailsAccess("project")).Get("/projects/new", projectHandler.Create)
		r.With(detailsAccess("project")).Post("/projects", projectHandler.Create)
		r.With(detailsAccess("project")).Post("/projects/inline", projectHandler.CreateInline)
		r.With(detailsAccess("project")).Get("/projects/{id}/edit", projectHandler.Update)
		r.With(detailsAccess("project")).Post("/projects/{id}", projectHandler.Update)
		r.With(detailsAccess("job")).Get("/jobs/new", jobHandler.Create)
		r.With(detailsAccess("job")).Post("/jobs", jobHandler.Create)
		r.With(detailsAccess("job")).Get("/jobs/{id}/edit", jobHandler.Update)
		r.With(detailsAccess("job")).Post("/jobs/{id}", jobHandler.Update)
		r.With(detailsAccess("asset")).Get("/assets/new", assetHandler.Create)
		r.With(detailsAccess("asset")).Post("/assets", assetHandler.Create)
		r.With(detailsAccess("asset")).Post("/assets/inline", assetHandler.CreateInline)
		r.With(detailsAccess("asset")).Get("/assets/{id}/edit", assetHandler.Update)
		r.With(detailsAccess("asset")).Post("/assets/{id}", assetHandler.Update)
		r.With(detailsAccess("estimate")).Get("/estimates/new", estimateHandler.Create)
		r.With(detailsAccess("estimate")).Post("/estimates", estimateHandler.Create)
		r.With(detailsAccess("invoice")).Get("/invoices/new", invoiceHandler.Create)
		r.With(detailsAccess("invoice")).Post("/invoices", invoiceHandler.Create)
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.With(activeObjectGuard(objectref.TypeProject)).Post("/projects/{id}/delete", projectHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/credit/refunds", customerHandler.RefundCredit)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Post("/customers/{id}/credit/refunds/{refund_id}/reverse", customerHandler.ReverseRefund)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/create-next-occurrence", jobHandler.CreateNextOccurrence)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/cancel-next-occurrence", jobHandler.CancelNextOccurrence)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/delete", jobHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/subtasks/{idx}/toggle", jobHandler.ToggleSubtask)
			r.Post("/schedule/dispatch", scheduleHandler.DispatchUpdate)
			r.Post("/schedule/move", scheduleHandler.CalendarMove)
			r.Post("/schedule/calendar/move", scheduleHandler.CalendarMove)
			r.With(activeObjectGuard(objectref.TypeAsset)).Post("/assets/{id}/delete", assetHandler.Delete)
			r.Get("/assets/locations", assetHandler.GetLocations)
		})

		// Assigned technicians may create documents from their Jobs and manage them.
		r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/create-invoice", invoiceHandler.CreateFromJob)
		r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/invoices", invoiceHandler.CreateForJob)
		r.With(activeObjectGuard(objectref.TypeJob)).Post("/jobs/{id}/create-estimate", estimateHandler.CreateFromJob)
		r.With(activeObjectGuard(objectref.TypeEstimate)).Get("/estimates/{id}/edit", estimateHandler.Update)
		r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}", estimateHandler.Update)
		r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/convert", estimateHandler.ConvertToInvoice)
		r.With(activeObjectGuard(objectref.TypeInvoice)).Get("/invoices/{id}/edit", invoiceHandler.Update)
		r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}", invoiceHandler.Update)
		r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/revert-to-estimate", invoiceHandler.RevertToEstimate)
		r.With(activeObjectGuard(objectref.TypeEstimate)).Get("/estimates/{id}/email", estimateHandler.Email)
		r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/email", estimateHandler.Email)
		r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/deliveries/{delivery_id}/retry", estimateHandler.RetryDelivery)
		r.Get("/estimates/{id}/pdf", estimateHandler.PDF)
		r.Get("/estimates/{id}/pdf/preview", estimateHandler.PreviewPDF)
		r.With(activeObjectGuard(objectref.TypeInvoice)).Get("/invoices/{id}/email", invoiceHandler.Email)
		r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/email", invoiceHandler.Email)
		r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/deliveries/{delivery_id}/retry", invoiceHandler.RetryDelivery)
		r.Get("/invoices/{id}/pdf", invoiceHandler.PDF)
		r.Get("/invoices/{id}/pdf/preview", invoiceHandler.PreviewPDF)
		r.Post("/invoices/{id}/pdf/save", invoiceHandler.SavePDF)
		r.Post("/estimates/{id}/pdf/save", estimateHandler.SavePDF)

		// Office-only billing and general document mutations.
		r.Group(func(r chi.Router) {
			r.Use(middleware.DispatcherOrAdmin)
			r.With(activeObjectGuard(objectref.TypeCustomer)).Get("/customers/{id}/create-invoice", invoiceHandler.CreateFromCustomer)
			r.With(activeObjectGuard(objectref.TypeEstimate)).Post("/estimates/{id}/delete", estimateHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/delete", invoiceHandler.Delete)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/finalize", invoiceHandler.Finalize)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/payments", invoiceHandler.RecordPayment)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/payments/{payment_id}/reverse", invoiceHandler.ReversePayment)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/credit-applications", invoiceHandler.ApplyCredit)
			r.With(activeObjectGuard(objectref.TypeInvoice)).Post("/invoices/{id}/credit-applications/{application_id}/reverse", invoiceHandler.ReverseCreditApplication)
		})

		// Vocabulary configuration and parent-bounded tagging have independent grants.
		r.With(vocabularyGuard("tags", false)).Get("/tags", tagHandler.List)
		r.With(vocabularyGuard("tags", true)).Get("/tags/new", tagHandler.Create)
		r.With(vocabularyGuard("tags", true)).Get("/tags/{id}/edit", tagHandler.Update)
		r.With(vocabularyGuard("tags", false)).Get("/tags/activity", activityHandler.ListByType(objectref.TypeTag))
		r.With(vocabularyGuard("tags", false)).Get("/tags/history", vocabularyHistory("tags"))
		r.With(vocabularyGuard("assets", false)).Get("/settings/assets/history", vocabularyHistory("assets"))
		r.With(vocabularyGuard("assets", false)).Get("/settings/assets", assetTypeHandler.Show)
		r.With(vocabularyGuard("assets", false)).Get("/settings/assets/activity", activityHandler.ListForAssetSettings)
		for _, v := range []struct{ prefix, kind string }{{"/tags", "tag"}, {"/settings/asset-types", "asset_type"}, {"/settings/asset-statuses", "asset_status"}} {
			r.Post(v.prefix, vocabularyMutation(v.kind, "save"))
			r.Post(v.prefix+"/{id}", vocabularyMutation(v.kind, "save"))
			for _, op := range []string{"delete", "retire", "reactivate"} {
				r.Post(v.prefix+"/{id}/"+op, vocabularyMutation(v.kind, op))
			}
		}
		for _, kind := range []string{"customer", "project", "job", "asset", "estimate", "invoice"} {
			r.Post("/"+kind+"s/{id}/tags/{tag_id}/attach", tagLinkMutation(kind, true, tagSvc, tagLinkSvc))
			r.Post("/"+kind+"s/{id}/tags/{tag_id}/detach", tagLinkMutation(kind, false, tagSvc, tagLinkSvc))
		}

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
			r.Post(e.prefix+"/{id}/comments", commentHandler.Create(e.objType))
			r.Post(e.prefix+"/{id}/comments/{cid}/delete", commentHandler.Delete(e.objType))
			r.Get(e.prefix+"/{id}/comments/{cid}/audit", commentHandler.Audit(e.objType))
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
		r.Get("/files/limits", fileHandler.Limits)
		r.Get("/files/storage-review", fileHandler.StorageReview)
		r.Get("/files/{id}", fileHandler.Download)
		r.Post("/files/{id}/rename", fileHandler.Rename)
		r.Post("/files/{id}/delete", fileHandler.Delete)

		r.Get("/settings/statuses/{type}", statusSettingsHandler.List)
		r.Post("/settings/statuses/{type}", statusSettingsHandler.Create)
		r.Post("/settings/statuses/{type}/{id}", statusSettingsHandler.Update)
		r.Post("/settings/statuses/{type}/{id}/default", statusSettingsHandler.Default)
		r.Post("/settings/statuses/{type}/{id}/move", statusSettingsHandler.Move)
		r.Post("/settings/statuses/{type}/{id}/delete", statusSettingsHandler.Delete)
		// Admin-only routes
		r.Get("/settings/custom-fields", cfHandler.List)
		r.Get("/settings/custom-fields/new", cfHandler.Create)
		r.Post("/settings/custom-fields", cfHandler.Create)
		r.Get("/settings/custom-fields/{id}/edit", cfHandler.Update)
		r.Post("/settings/custom-fields/{id}", cfHandler.Update)
		r.Post("/settings/custom-fields/{id}/delete", cfHandler.Delete)
		r.Group(func(r chi.Router) {
			r.Use(middleware.AdminOnly)
			// Restore archived entities
			r.Post("/jobs/{id}/restore", jobHandler.Restore)
			r.Post("/projects/{id}/restore", projectHandler.Restore)
			r.Post("/estimates/{id}/restore", estimateHandler.Restore)
			r.Post("/invoices/{id}/restore", invoiceHandler.Restore)
			r.Post("/assets/{id}/restore", assetHandler.Restore)
			// Admin activity feeds
			r.Get("/settings/activity", activityHandler.ListByType(objectref.TypeCompanySettings))
			r.Get("/settings/custom-fields/activity", activityHandler.ListByType(objectref.TypeCustomField))
			r.Get("/users/activity", activityHandler.ListByType(objectref.TypeUser))
			r.Get("/settings/advanced", settingsHandler.Show)
			r.Post("/settings/advanced", settingsHandler.Save)
			r.Get("/setup/company", settingsHandler.Show)
			r.Post("/setup/company", settingsHandler.Save)
			r.Post("/settings/enable-email", integrations.enableEmail)
			r.Get("/users", userHandler.List)
			r.Get("/users/new", userHandler.Create)
			r.Post("/users", userHandler.Create)
			r.Get("/users/{id}", userHandler.Show)
			r.Get("/users/{id}/edit", userHandler.Update)
			r.Post("/users/{id}", userHandler.Update)
			r.Post("/users/{id}/disable", userHandler.Disable)
			r.Post("/users/{id}/resend-welcome", userHandler.ResendWelcome)
			r.Post("/users/{id}/resend-recovery", userHandler.ResendRecovery)
			r.Post("/users/{id}/reset-password", userHandler.ResetPassword)
			r.Post("/users/{id}/require-change", authorityHandler.AccountSecurity)
			r.Post("/users/{id}/revoke-sessions", authorityHandler.AccountSecurity)
		})
	})

	return r
}
