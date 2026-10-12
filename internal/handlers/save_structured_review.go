package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/freefsm-project/freefsm/internal/services"
	"golang.org/x/net/html"
)

// Only the installed domain widget contracts below may restore hidden JSON.
// Transport identities/security fields and arbitrary hidden form values are
// never copied. A widget must exist in the freshly authorized form first.
func retainStructuredSaveInputs(form *html.Node, r *http.Request, comparison *strings.Builder) {
	walkSaveNodes(form, func(widget *html.Node) {
		if widget.Type != html.ElementNode || hasSaveAttr(widget, "disabled") {
			return
		}
		key, label := "", ""
		if hasSaveAttr(widget, "data-line-item-editor") {
			key, label = "line_items", "Document lines"
		}
		switch saveAttr(widget, "x-data") {
		case "visitsEditor()":
			key, label = "visits", "Job visits"
		case "assignmentsEditor()":
			key, label = "assignments", "Job assignments"
		case "subtasksEditor()":
			key, label = "subtasks", "Job subtasks"
		}
		if key == "" || !r.PostForm.Has(key) {
			return
		}
		var proposed any
		switch key {
		case "line_items":
			proposed = &[]services.LineItem{}
		case "visits":
			proposed = &[]services.JobVisit{}
		case "assignments":
			proposed = &[]services.JobAssignment{}
		case "subtasks":
			proposed = &[]services.JobSubtask{}
		}
		if err := json.Unmarshal([]byte(r.PostForm.Get(key)), proposed); err != nil {
			return
		}
		encoded, err := json.Marshal(proposed)
		if err != nil {
			return
		}
		if string(encoded) == "null" {
			encoded = []byte("[]")
		}
		old := saveAttr(widget, "data-existing")
		var bootstrap *html.Node
		walkSaveNodes(widget, func(n *html.Node) {
			if key == "line_items" && saveAttr(n, "data-line-item-json") == "existing" {
				bootstrap = n
				old = saveNodeText(n)
			}
			if n.Data == "input" && saveAttr(n, "name") == key {
				setSaveAttr(n, "value", string(encoded))
			}
		})
		comparison.WriteString("<dt>" + html.EscapeString(label) + "</dt><dd><pre>" + html.EscapeString(old) + "</pre></dd>")
		if key == "line_items" {
			if bootstrap == nil {
				return
			}
			for bootstrap.FirstChild != nil {
				bootstrap.RemoveChild(bootstrap.FirstChild)
			}
			// Marshal escapes script delimiters. html.Render intentionally leaves
			// script text raw; never insert unencoded submitted JSON here.
			bootstrap.AppendChild(&html.Node{Type: html.TextNode, Data: string(encoded)})
		} else {
			setSaveAttr(widget, "data-existing", string(encoded))
		}
		setSaveAttr(widget, "data-save-retained-structured", key)
	})
}
