package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func saveFormPath(path string) string {
	if !atomicSavePath(path) {
		return ""
	}
	if path == "/preferences" || path == "/settings" || path == "/settings/mail" || path == "/settings/maps" || path == "/settings/open-tracking" || path == "/setup/company" {
		return path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 3 && parts[0] == "jobs" && (parts[2] == "links" || parts[2] == "contacts" || parts[2] == "assets" || parts[2] == "dispatch") {
		return path
	}
	if len(parts) == 3 && parts[0] == "locations" && parts[2] == "coordinates" {
		return path
	}
	if len(parts) == 3 && parts[0] == "jobs" && parts[2] == "invoices" {
		return "/" + strings.Join(parts[:2], "/") + "/create-invoice"
	}
	if len(parts) >= 2 && parts[0] == "settings" {
		switch parts[1] {
		case "asset-types", "asset-statuses":
			return "/settings/assets"
		case "statuses":
			if len(parts) >= 3 {
				return "/" + strings.Join(parts[:3], "/")
			}
		case "custom-fields":
			if len(parts) == 2 {
				return path + "/new"
			}
			if len(parts) == 3 {
				return path + "/edit"
			}
		}
		return ""
	}
	if len(parts) >= 3 && parts[0] == "customers" && (parts[2] == "contacts" || parts[2] == "locations") {
		if len(parts) == 3 {
			return path + "/new"
		}
		if len(parts) == 4 {
			return path + "/edit"
		}
		return ""
	}
	if len(parts) == 3 && parts[2] == "custom-fields" {
		return path
	}
	if len(parts) > 2 {
		return ""
	}
	if len(parts) == 2 && parts[1] == "inline" {
		return ""
	} // dialog-owned command, not an ordinary edit form
	if len(parts) == 1 {
		return path + "/new"
	}
	return path + "/edit"
}

// Retention is derived from a freshly authorized form, not from arbitrary POST
// keys or an old cached page. Removed facets and password/file inputs cannot be
// redisclosed. Dynamic structured widgets keep their existing domain-specific
// error renderers; this adapter covers ordinary controls in installed forms.
func retainAuthorizedSaveForm(w *saveResponse, r *http.Request, current http.Handler) {
	if w.streaming || bytes.Contains(w.body.Bytes(), []byte("<form")) || strings.Contains(r.Header.Get("Accept"), "application/json") {
		return
	}
	path := saveFormPath(r.URL.Path)
	if path == "" {
		return
	}
	get := r.Clone(context.WithValue(saveaction.WithoutAction(r.Context()), chi.RouteCtxKey, chi.NewRouteContext()))
	u := *r.URL
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	get.URL = &u
	get.RequestURI = path
	get.Method = http.MethodGet
	get.Body = http.NoBody
	get.ContentLength = 0
	get.Form = nil
	get.PostForm = nil
	get.MultipartForm = nil
	get.Header = r.Header.Clone()
	for _, key := range []string{"Content-Type", "Content-Length", "X-FreeFSM-Action", "X-FreeFSM-Revision", "HX-Request", "HX-Boosted"} {
		get.Header.Del(key)
	}
	buffer := &formReviewResponse{header: make(http.Header)}
	current.ServeHTTP(buffer, get)
	if buffer.status == 403 || buffer.status == 404 || buffer.status == 401 {
		w.status = buffer.status
		w.header.Set("Content-Type", "text/plain; charset=utf-8")
		w.body.Reset()
		w.body.WriteString("Access changed. Inaccessible submitted input has been removed.")
		return
	}
	if buffer.status != 0 && buffer.status != 200 {
		return
	}
	doc, err := html.Parse(bytes.NewReader(buffer.body.Bytes()))
	if err != nil {
		return
	}
	var target *html.Node
	_ = r.ParseForm()
	walkSaveNodes(doc, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "form" {
			return
		}
		action := saveAttr(n, "action")
		u, err := url.Parse(action)
		if err != nil {
			return
		}
		if get.URL.ResolveReference(u).Path == r.URL.Path {
			if section := r.FormValue("section"); section != "" {
				match := false
				walkSaveNodes(n, func(field *html.Node) {
					if saveAttr(field, "name") == "section" && saveAttr(field, "value") == section {
						match = true
					}
				})
				if !match {
					return
				}
			}
			target = n
		}
	})
	if target == nil {
		return
	}
	_ = r.ParseForm()
	var comparison strings.Builder
	comparison.WriteString(`<section class="save-error-summary" role="alert" tabindex="-1" data-error-summary><h2>Save was not applied</h2><p>Review the current authorized values below, revise your retained input, or discard it. Nothing will overwrite the current record until you explicitly submit the reviewed correction.</p><details open><summary>Current authorized values</summary><dl>`)
	retainStructuredSaveInputs(target, r, &comparison)
	newRevision := ""
	nativeRevision := ""
	walkSaveNodes(target, func(n *html.Node) {
		name := saveAttr(n, "name")
		if name == "" {
			return
		}
		if name == "save_revision" {
			newRevision = saveAttr(n, "value")
			setSaveAttr(n, "value", saveaction.SubmittedRevision(r.Context()))
			return
		}
		if name == "revision" && r.PostForm.Has("revision") {
			nativeRevision = saveAttr(n, "value")
			setSaveAttr(n, "value", r.FormValue("revision"))
			return
		}
		if n.Data != "input" && n.Data != "textarea" && n.Data != "select" {
			return
		}
		typ := saveAttr(n, "type")
		if typ == "hidden" || typ == "password" || typ == "file" || typ == "submit" || hasSaveAttr(n, "disabled") || hasSaveAttr(n, "readonly") {
			return
		}
		old := saveAttr(n, "value")
		choiceValue := old
		if typ == "checkbox" || typ == "radio" {
			if !hasSaveAttr(n, "value") {
				choiceValue = "on"
			}
			state := "Not checked"
			if hasSaveAttr(n, "checked") {
				state = "Checked"
			}
			if typ == "radio" {
				state = "Not selected"
				if hasSaveAttr(n, "checked") {
					state = "Selected"
				}
			}
			old = state + " (value: " + choiceValue + ")"
		}
		if n.Data == "textarea" {
			old = saveNodeText(n)
		}
		if n.Data == "select" {
			old = currentSaveSelection(n)
		}
		comparison.WriteString("<dt>" + html.EscapeString(name) + "</dt><dd>" + html.EscapeString(old) + "</dd>")
		values, present := r.PostForm[name]
		if typ == "checkbox" || typ == "radio" {
			removeSaveAttr(n, "checked")
			for _, v := range values {
				if v == choiceValue {
					setSaveAttr(n, "checked", "")
				}
			}
			return
		}
		if !present {
			return
		}
		value := ""
		if len(values) > 0 {
			value = values[0]
		}
		switch n.Data {
		case "input":
			setSaveAttr(n, "value", value)
		case "textarea":
			for n.FirstChild != nil {
				n.RemoveChild(n.FirstChild)
			}
			n.AppendChild(&html.Node{Type: html.TextNode, Data: value})
		case "select":
			walkSaveNodes(n, func(o *html.Node) {
				if o.Data != "option" {
					return
				}
				removeSaveAttr(o, "selected")
				for _, v := range values {
					if v == saveOptionValue(o) {
						setSaveAttr(o, "selected", "")
					}
				}
			})
		}
	})
	comparison.WriteString(`</dl></details><p><a href="` + html.EscapeString(path) + `">Discard submitted input and reload</a></p></section>`)
	fragment, err := html.ParseFragment(strings.NewReader(comparison.String()), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return
	}
	for _, n := range fragment {
		target.InsertBefore(n, target.FirstChild)
	}
	if newRevision != "" || nativeRevision != "" {
		// A named submitter is explicit review; ordinary submit/Enter keeps the
		// stale baseline. The next commit rechecks this reviewed revision too.
		name, value := "save_conflict_review", newRevision
		if newRevision == "" {
			name, value = "save_native_revision_review", nativeRevision
		} else if nativeRevision != "" {
			value = newRevision + "." + url.QueryEscape(nativeRevision)
		}
		button := &html.Node{Type: html.ElementNode, Data: "button", DataAtom: atom.Button, Attr: []html.Attribute{{Key: "type", Val: "submit"}, {Key: "name", Val: name}, {Key: "value", Val: value}}}
		button.AppendChild(&html.Node{Type: html.TextNode, Data: "Save reviewed correction"})
		target.AppendChild(button)
	}
	w.body.Reset()
	if err := html.Render(&w.body, doc); err != nil {
		return
	}
	w.header.Set("Content-Type", "text/html; charset=utf-8")
	w.header.Set("X-FreeFSM-Form", "review")
	w.header.Del("Content-Length")
	w.enhanced = true
}

func saveOptionValue(option *html.Node) string {
	if hasSaveAttr(option, "value") {
		return saveAttr(option, "value")
	}
	return strings.Join(strings.Fields(saveNodeText(option)), " ")
}

func currentSaveSelection(selectNode *html.Node) string {
	selected := []string{}
	var first *html.Node
	walkSaveNodes(selectNode, func(option *html.Node) {
		if option.Data != "option" {
			return
		}
		disabled := hasSaveAttr(option, "disabled") || option.Parent != nil && option.Parent.Data == "optgroup" && hasSaveAttr(option.Parent, "disabled")
		if first == nil && !disabled {
			first = option
		}
		if hasSaveAttr(option, "selected") {
			selected = append(selected, saveNodeText(option))
		}
	})
	if !hasSaveAttr(selectNode, "multiple") {
		if len(selected) > 1 {
			selected = selected[len(selected)-1:]
		}
		// A normal single-select defaults to its first enabled option. A list
		// box or multiselect with no selected options genuinely has no selection.
		if len(selected) == 0 && first != nil && (saveAttr(selectNode, "size") == "" || saveAttr(selectNode, "size") == "1") {
			selected = append(selected, saveNodeText(first))
		}
	}
	if len(selected) == 0 {
		return "None selected"
	}
	return strings.Join(selected, ", ")
}

type formReviewResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *formReviewResponse) Header() http.Header { return w.header }
func (w *formReviewResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *formReviewResponse) Write(b []byte) (int, error) {
	if w.body.Len()+len(b) > 4<<20 {
		return 0, io.ErrShortBuffer
	}
	return w.body.Write(b)
}
func walkSaveNodes(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkSaveNodes(c, fn)
	}
}
func saveAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func hasSaveAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}
func setSaveAttr(n *html.Node, key, value string) {
	removeSaveAttr(n, key)
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: value})
}
func removeSaveAttr(n *html.Node, key string) {
	for i := len(n.Attr) - 1; i >= 0; i-- {
		if n.Attr[i].Key == key {
			n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
		}
	}
}
func saveNodeText(n *html.Node) string {
	var b strings.Builder
	walkSaveNodes(n, func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
	})
	return b.String()
}
