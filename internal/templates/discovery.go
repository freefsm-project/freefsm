package templates

import (
	"net/url"
	"slices"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

type DiscoveryPageData struct {
	Page   authorization.DiscoveryPage
	Values url.Values
}

func (p DiscoveryPageData) Selected(typ string) bool { return slices.Contains(p.Values["type"], typ) }
func (p DiscoveryPageData) PageURL(page int) string {
	return (ListPresentation{Key: "search", Values: p.Values}).PageURL(page)
}
func (p DiscoveryPageData) Summary() string {
	return strconv.Itoa(p.Page.Total) + " results"
}
