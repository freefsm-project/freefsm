package templates

import "net/url"

func scheduleURL(p SchedulePageData, tab, period, date string) string {
	q := url.Values{"tab": {tab}, "period": {period}, "date": {date}, "q": {p.Query}, "status": {p.StatusFilter}, "assignment": {p.AssignmentFilter}, "sort": {p.Sort}}
	return "/schedule?" + q.Encode()
}
