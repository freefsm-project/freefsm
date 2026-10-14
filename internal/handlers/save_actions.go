package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	xhtml "golang.org/x/net/html"
)

// guardedOnlineActions installs identities on server forms and accepts the same
// protocol from fetch actions. It never caches/replays sensitive responses or
// wraps independent domain transactions in a pretend atomic HTTP transaction.
func guardedOnlineActions(s *authorization.Service, cfg *config.Config, current http.Handler) func(http.Handler) http.Handler {
	store := saveaction.Store{Pool: s.Pool}
	key := saveProtectionKey(cfg.SessionSecret)
	maxUpload := cfg.MaxUploadSize
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Pairing has a browser-bound, once-only protocol of its own. Never
			// journal its secrets as a generic domain save or offer save replay.
			if r.URL.Path == "/pair" || strings.HasPrefix(r.URL.Path, "/pair/") {
				next.ServeHTTP(w, r)
				return
			}
			history := saveHistoryTag(key, authorizationActor(r))
			r = r.WithContext(context.WithValue(r.Context(), saveHistoryContextKey{}, history))
			capture := &saveResponse{ResponseWriter: w, header: w.Header().Clone()}
			capture.Header().Set("X-FreeFSM-Save-Protocol", "1")
			capture.Header().Set("X-FreeFSM-History", history)
			// Capture before the page's records are read. Capturing after rendering
			// could pair old displayed values with a newer valid revision.
			pageRevision := ""
			if r.Method == http.MethodGet {
				pageRevision, _ = saveaction.Revision(r.Context(), s.Pool, authorizationActor(r).UserID, r.URL.Path)
			}
			var action saveaction.Action
			if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
				id := r.Header.Get("X-FreeFSM-Action")
				revision := r.Header.Get("X-FreeFSM-Revision")
				// Reading is bounded even for upload routes. Restore the exact body for
				// domain parsers; no submitted values are persisted in the journal.
				limit := maxUpload + (1 << 20)
				if limit < 2<<20 {
					limit = 2 << 20
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
				if err != nil || int64(len(body)) > limit {
					http.Error(w, "Request body unavailable or too large", http.StatusRequestEntityTooLarge)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if media == "multipart/form-data" {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						http.Error(w, "Invalid upload form", 400)
						return
					}
					if r.MultipartForm != nil {
						defer r.MultipartForm.RemoveAll()
					}
				}
				if media == "application/x-www-form-urlencoded" || media == "multipart/form-data" {
					if err := r.ParseForm(); err != nil {
						http.Error(w, "Invalid form", 400)
						return
					}
					// CSRF middleware may already have parsed/consumed the body.
					// Use PostForm so an empty raw stream cannot erase intent.
					values := r.PostForm
					if id == "" {
						id = values.Get("save_action_id")
					}
					if revision == "" {
						revision = values.Get("save_revision")
					}
					// Only the explicitly labelled conflict-review submitter advances
					// the revision. Ordinary Save retains the rejected old baseline.
					if reviewed := values.Get("save_conflict_review"); reviewed != "" {
						record, native, paired := strings.Cut(reviewed, ".")
						revision = record
						if paired {
							decoded, err := url.QueryUnescape(native)
							if err != nil {
								http.Error(w, "Invalid reviewed revision", 400)
								return
							}
							r.PostForm.Set("revision", decoded)
							r.Form.Set("revision", decoded)
						}
					}
					if reviewed := values.Get("save_native_revision_review"); reviewed != "" {
						r.PostForm.Set("revision", reviewed)
						r.Form.Set("revision", reviewed)
					}
				}
				if id != "" {
					if _, err := uuid.Parse(id); err != nil {
						http.Error(w, "Invalid save identity", 400)
						return
					}
					action = saveaction.Action{UserID: authorizationActor(r).UserID, ID: id, Revision: revision, Path: r.URL.Path, Outcome: &saveaction.Outcome{}, Atomic: atomicSavePath(r.URL.Path), NoChange: noChangeSavePath(r.URL.Path)}
					pageRevision = revision
					capture.Header().Set("X-FreeFSM-Action", id)
					capture.Header().Set("X-FreeFSM-Receipt", "/save-actions/"+id)
					capture.Header().Set("X-FreeFSM-Disposition", "unknown")
					// An old page must not recreate a missing post-restore identity.
					// Existing receipts still reconcile across sign-in/key changes.
					if supplied := submittedSaveHistory(r); supplied != "" && !sameSaveHistory(r, supplied) {
						receipt, err := store.Receipt(r.Context(), action)
						if err != nil {
							http.Error(w, "Save status unavailable. Check the same action again.", 503)
							return
						}
						if receipt.Disposition == "not_received" {
							capture.Header().Set("Content-Type", "application/json")
							capture.Header().Set("X-FreeFSM-Disposition", "review")
							capture.WriteHeader(http.StatusConflict)
							_ = json.NewEncoder(capture).Encode(classifySaveHistory(r, receipt))
							capture.finish(nil)
							return
						}
					}
					digest, err := saveIntent(r, body, media, revision, key)
					if err != nil {
						http.Error(w, "Cannot read submitted content. No action dispatched.", 400)
						return
					}
					review := saveReviewPath(r)
					admitted, err := store.Admit(r.Context(), action, digest, r.Method, r.URL.Path, review)
					if err != nil && !errors.Is(err, saveaction.ErrIdentity) {
						http.Error(w, "Save status unavailable. Keep this action identity and check again.", 503)
						return
					}
					if !admitted {
						receipt, lookupErr := store.Receipt(r.Context(), action)
						if lookupErr != nil {
							http.Error(w, "Save status unavailable. Check the same action again.", 503)
							return
						}
						if errors.Is(err, saveaction.ErrIdentity) {
							receipt.Disposition = "identity_conflict"
						}
						if saveFormPath(receipt.RequestPath) == "" {
							receipt.InputAccessURL = ""
						}
						capture.Header().Set("Content-Type", "application/json")
						capture.Header().Set("X-FreeFSM-Disposition", receipt.Disposition)
						capture.WriteHeader(http.StatusConflict)
						_ = json.NewEncoder(capture).Encode(receipt)
						capture.finish(nil)
						return
					}
					r = r.WithContext(saveaction.WithAction(r.Context(), action))
					// Transport metadata is not a domain field. Strict domain input
					// allowlists receive exactly their own form values.
					for _, key := range []string{"save_action_id", "save_history", "save_revision", "save_conflict_review", "save_native_revision_review"} {
						r.Form.Del(key)
						r.PostForm.Del(key)
						if r.MultipartForm != nil {
							delete(r.MultipartForm.Value, key)
						}
					}
					if media == "application/x-www-form-urlencoded" {
						forwarded := r.PostForm.Encode()
						r.Body = io.NopCloser(strings.NewReader(forwarded))
						r.ContentLength = int64(len(forwarded))
						r.Header.Set("Content-Length", fmt.Sprint(len(forwarded)))
					}
				}
			}
			next.ServeHTTP(capture, r)
			if action.ID != "" {
				// Client disconnect must not prevent recording a known completed
				// outcome. Process death still leaves a durable unknown admission.
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				defer cancel()
				location := safeSaveLocation(capture.Header().Get("Location"))
				if location == "" {
					location = safeSaveLocation(capture.Header().Get("X-FreeFSM-Result"))
				}
				_ = store.Complete(ctx, action, capture.statusCode(), location, atomicSavePath(r.URL.Path))
				if receipt, err := store.Receipt(ctx, action); err == nil {
					capture.Header().Set("X-FreeFSM-Disposition", receipt.Disposition)
					if receipt.RetrySafe && capture.statusCode() != 403 && capture.statusCode() != 404 {
						retainAuthorizedSaveForm(capture, r, current)
					}
				}
			}
			capture.finish(func(body []byte) []byte {
				return installSaveForms(body, func(action string) string {
					path := r.URL.Path
					if action != "" {
						if u, err := url.Parse(action); err == nil {
							path = r.URL.ResolveReference(u).Path
						}
					}
					if saveParentPath(path) == saveParentPath(r.URL.Path) {
						return pageRevision
					}
					return ""
				}, history)
			})
		})
	}
}

// These installed form commands perform their primary mutation in the shared
// authorization/DetailsTransaction boundary. Legacy multi-step actions are
// deliberately excluded from rollback-to-revise assurances.
func atomicSavePath(path string) bool {
	if path == "/preferences" || path == "/settings" || path == "/setup/company" || path == "/settings/mail" || path == "/settings/maps" || path == "/settings/open-tracking" {
		return true
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 2 && parts[0] == "time-entries" {
		id, err := strconv.ParseInt(parts[1], 10, 64)
		return err == nil && id > 0
	}
	if len(parts) == 3 && parts[0] == "locations" && parts[2] == "coordinates" {
		return true
	}
	if len(parts) == 3 && parts[0] == "jobs" && (parts[2] == "invoices" || parts[2] == "create-estimate" || parts[2] == "links" || parts[2] == "contacts" || parts[2] == "dispatch") {
		return true
	}
	if len(parts) >= 3 && parts[0] == "customers" && (parts[2] == "contacts" || parts[2] == "locations") {
		return true
	}
	if strings.HasPrefix(path, "/settings/statuses/") || strings.HasPrefix(path, "/settings/asset-types") || strings.HasPrefix(path, "/settings/asset-statuses") || strings.HasPrefix(path, "/settings/custom-fields") {
		return true
	}
	if len(parts) == 3 && (parts[2] == "custom-fields" || parts[2] == "status") {
		return true
	}
	if len(parts) > 2 {
		return false
	}
	switch parts[0] {
	case "items", "customers", "jobs", "projects", "assets", "estimates", "invoices", "contacts", "locations", "tags":
		return true
	}
	return false
}

func noChangeSavePath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && parts[0] == "jobs" && parts[2] == "dispatch" {
		return true
	}
	if len(parts) == 3 && parts[2] == "status" {
		switch parts[0] {
		case "jobs", "projects", "estimates", "invoices":
			return true
		}
	}
	return len(parts) == 5 && parts[0] == "customers" && (parts[2] == "contacts" || parts[2] == "locations") && (parts[4] == "delete" || parts[4] == "restore")
}

func saveParentPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 4 && parts[0] == "customers" && (parts[2] == "contacts" || parts[2] == "locations") {
		return strings.Join(parts[:4], "/")
	}
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}

// Ignore transport-only CSRF tokens and multipart boundary encodings when
// comparing an intent. A browser may refresh CSRF or choose a new boundary while
// reconciling the same action. File bytes still participate in the digest.
func saveIntent(r *http.Request, body []byte, media, revision string, key []byte) (string, error) {
	// Bodies may contain passwords and SMTP credentials. A database-only reader
	// must not acquire a cheap offline credential verifier from this journal.
	h := hmac.New(sha256.New, key)
	h.Write([]byte("freefsm/online-save/intent/v1\x00"))
	fmt.Fprintf(h, "%s\n%s\n%s\n%s\n", r.Method, r.URL.RequestURI(), media, revision)
	if media == "application/x-www-form-urlencoded" || media == "multipart/form-data" {
		v := url.Values{}
		for key, values := range r.PostForm {
			if key != "csrf_token" && key != "save_action_id" && key != "save_history" {
				v[key] = values
			}
		}
		body = []byte(v.Encode())
	}
	h.Write(body)
	if media == "multipart/form-data" && r.MultipartForm != nil {
		keys := make([]string, 0, len(r.MultipartForm.File))
		for key := range r.MultipartForm.File {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			for _, file := range r.MultipartForm.File[key] {
				fmt.Fprintf(h, "\n%q:%q:%d:%q\n", key, file.Filename, file.Size, file.Header.Get("Content-Type"))
				f, err := file.Open()
				if err != nil {
					return "", err
				}
				_, err = io.Copy(h, f)
				closeErr := f.Close()
				if err != nil {
					return "", err
				}
				if closeErr != nil {
					return "", closeErr
				}
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func safeSaveLocation(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return ""
	}
	// Flash messages and other query values may contain submitted private data.
	return u.EscapedPath()
}

func saveReviewPath(r *http.Request) string {
	if raw := r.Header.Get("Referer"); raw != "" {
		if u, err := url.Parse(raw); err == nil && (u.Host == "" || u.Host == r.Host) {
			if path := safeSaveLocation(u.Path); path != "" {
				return path
			}
		}
	}
	if path := saveFormPath(r.URL.Path); path != "" {
		return path
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) > 0 && parts[0] == "files" {
		if id, err := strconv.ParseInt(r.FormValue("object_id"), 10, 64); err == nil && id > 0 {
			kind := r.FormValue("object_type")
			switch kind {
			case "customer", "job", "project", "asset", "estimate", "invoice":
				return fmt.Sprintf("/%ss/%d", kind, id)
			}
		}
		if len(parts) >= 2 {
			return "/files/" + parts[1]
		}
		return "/"
	}
	if len(parts) >= 3 && parts[0] == "locations" && parts[2] == "coordinates" {
		return "/" + strings.Join(parts[:3], "/")
	}
	if len(parts) > 0 && parts[0] == "dashboard" {
		return "/"
	}
	if len(parts) > 0 && parts[0] == "schedule" {
		return "/schedule"
	}
	if len(parts) >= 2 && parts[0] == "settings" {
		if parts[1] == "test-email" || parts[1] == "enable-email" {
			return "/settings/mail"
		}
		if parts[1] == "invoice-logo" || parts[1] == "numbering-review" {
			return "/settings"
		}
		if parts[1] == "asset-types" || parts[1] == "asset-statuses" {
			return "/settings/assets"
		}
		if parts[1] == "statuses" && len(parts) >= 3 {
			return "/" + strings.Join(parts[:3], "/")
		}
		return "/" + strings.Join(parts[:2], "/")
	}
	if len(parts) >= 4 && parts[0] == "customers" && (parts[2] == "contacts" || parts[2] == "locations") {
		return "/" + strings.Join(parts[:4], "/")
	}
	return "/" + saveParentPath(r.URL.Path)
}

func saveReceiptHandler(s *authorization.Service, review bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Auth middleware validates a current active session. Receipt ownership is
		// intrinsic Own access, surviving loss of the original resource grant.
		a := saveaction.Action{UserID: authorizationActor(r).UserID, ID: chi.URLParam(r, "operation")}
		receipt, err := (saveaction.Store{Pool: s.Pool}).Receipt(r.Context(), a)
		if errors.Is(err, saveaction.ErrIdentity) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "Save status unavailable. Check this action again.", 503)
			return
		}
		historyReceipt := classifySaveHistory(r, receipt)
		receipt = historyReceipt.Receipt
		if !review {
			if saveFormPath(receipt.RequestPath) == "" {
				receipt.InputAccessURL = ""
			}
			w.Header().Set("Content-Type", "application/json")
			historyReceipt.Receipt = receipt
			_ = json.NewEncoder(w).Encode(historyReceipt)
			return
		}
		if receipt.Disposition == "not_received" {
			http.NotFound(w, r)
			return
		}
		component := templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
			message := "An interrupted or partially completed action must not be submitted again. Review the current record before deciding what remains to be done. Dismissing this message does not resolve the action."
			if historyReceipt.HistoryChanged {
				message = "The original sign-in/history context is no longer available. This action may have completed before history changed. Review current records before deciding what remains to be done."
			}
			if receipt.Disposition == "saved" {
				message = "The recorded domain command is confirmed. Open the current authorized result to review it; this action will not be repeated."
			}
			if receipt.Disposition == "completed" {
				message = "The authorized request completed without a domain change. Open the current form or record to review the current result."
			}
			if receipt.Disposition == "rejected" {
				message = "This command was rejected and not applied. Review the current form before explicitly correcting or discarding your input."
			}
			if _, err := fmt.Fprintf(out, `<section><h1>Review current state</h1><p role="status">Action status: %s. Recorded domain effects: %d.</p><p>%s</p><p><a href="%s">Open current authorized form or record</a></p><ul>`, html.EscapeString(receipt.Disposition), receipt.CommittedSteps, html.EscapeString(message), html.EscapeString(receipt.ReviewPath)); err != nil {
				return err
			}
			for _, effect := range receipt.Effects {
				label := fmt.Sprintf("%s: %s #%d", effect.Action, effect.TargetType, effect.TargetID)
				if _, err := fmt.Fprintf(out, "<li>%s</li>", html.EscapeString(label)); err != nil {
					return err
				}
			}
			_, err := fmt.Fprintf(out, `</ul><p><a href="/save-actions/%s">Check this same action receipt</a></p></section>`, html.EscapeString(a.ID))
			return err
		})
		render(w, r, templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
			return templates.Layout("Review save").Render(templ.WithChildren(ctx, component), out)
		}))
	}
}

// Bounded response buffering permits metadata and form enhancement without
// retaining a response cache. Large/download responses stream unchanged.
type saveResponse struct {
	http.ResponseWriter
	header    http.Header
	body      bytes.Buffer
	status    int
	streaming bool
	enhanced  bool
}

func (w *saveResponse) Header() http.Header { return w.header }
func (w *saveResponse) statusCode() int {
	if w.status == 0 {
		return 200
	}
	return w.status
}
func (w *saveResponse) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}
func (w *saveResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.streaming {
		return w.ResponseWriter.Write(p)
	}
	if w.body.Len()+len(p) > 4<<20 {
		w.flush()
		return w.ResponseWriter.Write(p)
	}
	return w.body.Write(p)
}
func (w *saveResponse) flush() {
	for k := range w.ResponseWriter.Header() {
		w.ResponseWriter.Header().Del(k)
	}
	for k, v := range w.header {
		w.ResponseWriter.Header()[k] = v
	}
	w.ResponseWriter.WriteHeader(w.statusCode())
	w.streaming = true
	_, _ = w.ResponseWriter.Write(w.body.Bytes())
	w.body.Reset()
}
func (w *saveResponse) finish(transform func([]byte) []byte) {
	if w.streaming {
		return
	}
	if !w.enhanced && transform != nil && (strings.Contains(w.header.Get("Content-Type"), "text/html") || bytes.Contains(w.body.Bytes(), []byte("<form"))) {
		b := transform(w.body.Bytes())
		w.body.Reset()
		w.body.Write(b)
		w.header.Del("Content-Length")
	}
	w.flush()
}

func installSaveForms(body []byte, revision func(string) string, history string) []byte {
	z := xhtml.NewTokenizer(bytes.NewReader(body))
	var out bytes.Buffer
	for {
		typ := z.Next()
		if typ == xhtml.ErrorToken {
			break
		}
		raw := append([]byte(nil), z.Raw()...)
		out.Write(raw)
		if typ != xhtml.StartTagToken {
			continue
		}
		token := z.Token()
		if token.Data == "head" {
			fmt.Fprintf(&out, `<meta name="freefsm-save-history" content="%s">`, html.EscapeString(history))
		}
		if token.Data != "form" {
			continue
		}
		method, action := "", ""
		for _, a := range token.Attr {
			switch a.Key {
			case "method":
				method = a.Val
			case "action":
				action = a.Val
			case "hx-post":
				method, action = "post", a.Val
			}
		}
		if !strings.EqualFold(method, "post") {
			continue
		}
		if action != "" {
			u, err := url.Parse(action)
			if err != nil || u.IsAbs() || u.Host != "" {
				continue
			}
		}
		fmt.Fprintf(&out, `<input type="hidden" name="save_action_id" value="%s">`, uuid.NewString())
		fmt.Fprintf(&out, `<input type="hidden" name="save_history" value="%s">`, html.EscapeString(history))
		if value := revision(action); value != "" {
			fmt.Fprintf(&out, `<input type="hidden" name="save_revision" value="%s">`, html.EscapeString(value))
		}
	}
	return out.Bytes()
}
