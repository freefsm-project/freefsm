package customfields

import "slices"

// Parent is the closed collaboration vocabulary. An empty Table denotes a
// future domain that owns its record integration, not a speculative SQL table.
type Parent struct{ Type, Table string }

var parents = []Parent{
	{"customer", "customers"}, {"project", "projects"}, {"job", "jobs"},
	{"asset", "assets"}, {"estimate", "estimates"}, {"invoice", "invoices"}, {"lease", "leases"},
}
var types = []string{"text", "number", "date", "textarea", "select", "checkbox", "multiselect"}

func ParentTypes() []string {
	out := make([]string, 0, len(parents))
	for _, p := range parents {
		out = append(out, p.Type)
	}
	return out
}
func Types() []string { return slices.Clone(types) }
func SupportsParent(typ string) bool {
	for _, p := range parents {
		if p.Type == typ {
			return true
		}
	}
	return false
}
func SupportsType(typ string) bool { return slices.Contains(types, typ) }
func ExistingParents() []Parent {
	out := make([]Parent, 0, len(parents))
	for _, p := range parents {
		if p.Table != "" {
			out = append(out, p)
		}
	}
	return out
}
func ParentTable(typ string) (string, bool) {
	for _, p := range parents {
		if p.Type == typ && p.Table != "" {
			return p.Table, true
		}
	}
	return "", false
}
