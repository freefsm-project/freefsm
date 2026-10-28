package templates

import "github.com/freefsm-project/freefsm/internal/authorization"

type LeasePageData struct {
	Error         string
	Base          authorization.DraftLeaseBase
	Choices       authorization.LeaseChoices
	Tags, AllTags []TagRow
	Fields        []CustomFieldDisplay
	Files         FileListPageData
}

func leaseChoiceContains(choices []authorization.LeaseChoice, id int64) bool {
	for _, c := range choices {
		if c.ID == id {
			return true
		}
	}
	return false
}
