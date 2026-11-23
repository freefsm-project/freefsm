package templates

import (
	"net/url"
	"strconv"
)

func operationalListScope(customerID int64, scope, sort string) url.Values {
	q := customerScope(customerID)
	q.Set("scope", scope)
	q.Set("sort", sort)
	return q
}

func operationalExportURL(path, search string, statusID, customerID int64, valid, scope, sort string) string {
	q := operationalListScope(customerID, scope, sort)
	q.Set("search", search)
	q.Set("status_id", strconv.FormatInt(statusID, 10))
	if valid != "" {
		q.Set("valid", valid)
	}
	return path + "/export?" + q.Encode()
}

// WorkReportPageData is a presentation of one authorized report snapshot.
// Missing snapshots carry an error, never fabricated empty totals.
type WorkReportPageData struct {
	Title, Path, Description, UpdatedAt, Timezone, Error string
	Filters                                              url.Values
	Fields                                               []WorkReportField
	Scopes                                               []string
	Summary                                              []WorkReportMeasure
	Groups                                               []WorkReportGroup
	Headers                                              []string
	RelatedHeader                                        string
	Rows                                                 []WorkReportRow
	ExportURL                                            string
	Nonadditive                                          bool
}

type WorkReportField struct {
	Name, Label, Type string
	Options           []WorkReportOption
}
type WorkReportOption struct{ Value, Label string }
type WorkReportMeasure struct{ Label, Value, URL string }
type WorkReportGroup struct {
	Label    string
	Measures []WorkReportMeasure
}
type WorkReportRow struct {
	Label, URL               string
	RelatedLabel, RelatedURL string
	Cells                    []string
}

func workReportURL(path string, filters url.Values, key, value string) string {
	q := url.Values{}
	for k, values := range filters {
		q[k] = append([]string(nil), values...)
	}
	if key != "" {
		q.Set(key, value)
	}
	return path + "?" + q.Encode()
}
