package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func (h *JobHandler) Dispatch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if r.Method == http.MethodGet {
		saveaction.ReadOnly(r.Context())
	}
	form, err := req.Service.DispatchForm(r.Context(), req.Actor, id)
	if err != nil {
		jobRelationshipError(w, r, err)
		return
	}
	form.Draft = map[string]string{}
	failInput := func(err error) {
		form.Error = err.Error()
		for key, values := range r.PostForm {
			if len(values) == 1 {
				form.Draft[key] = values[0]
			}
		}
		for i := range form.Choices {
			form.Choices[i].Selected = false
			for _, v := range r.PostForm["assignee_ids"] {
				if v == fmt.Sprint(form.Choices[i].ID) {
					form.Choices[i].Selected = true
				}
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		render(w, r, templates.JobDispatch(id, form))
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid dispatch", 400)
			return
		}
		if review := r.PostForm.Get("review_id"); review != "" {
			err = req.Service.ConfirmDispatch(r.Context(), req.Actor, id, review, r.PostForm.Get("acknowledge_overlaps") == "true")
			if err != nil {
				if errors.Is(err, authorization.ErrConflict) || errors.Is(err, saveaction.ErrStale) {
					current, loadErr := req.Service.DispatchForm(r.Context(), req.Actor, id)
					if loadErr != nil {
						dispatchError(w, r, loadErr)
						return
					}
					proposed, loadErr := req.Service.DispatchDraft(r.Context(), req.Actor, id, review)
					if loadErr != nil {
						dispatchError(w, r, loadErr)
						return
					}
					for _, u := range current.Choices {
						if u.Selected {
							current.CurrentUsers = append(current.CurrentUsers, u.Name)
						}
					}
					copyOfCurrent := current.Plan
					current.CurrentPlan = &copyOfCurrent
					if !current.CanSchedule {
						proposed.Start, proposed.End, proposed.ArrivalStart, proposed.ArrivalEnd = copyOfCurrent.Start, copyOfCurrent.End, copyOfCurrent.ArrivalStart, copyOfCurrent.ArrivalEnd
					}
					if !current.CanAssign {
						proposed.Assignees = copyOfCurrent.Assignees
					}
					current.Plan = proposed
					for i := range current.Choices {
						current.Choices[i].Selected = false
						for _, uid := range proposed.Assignees {
							if current.Choices[i].ID == uid {
								current.Choices[i].Selected = true
							}
						}
					}
					current.Error = "Dispatch was not saved. The Job, assignee availability or overlaps changed, the review expired, or the current overlaps were not acknowledged. Compare the current values below, revise or discard your proposal, and review again."
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(409)
					render(w, r, templates.JobDispatch(id, current))
					return
				}
				dispatchError(w, r, err)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/jobs/%d?flash=Dispatch+saved", id), 303)
			return
		}
		saveaction.ReadOnly(r.Context())
		in := authorization.DispatchPlan{}
		for name, dest := range map[string]**time.Time{"start": &in.Start, "end": &in.End, "arrival_start": &in.ArrivalStart, "arrival_end": &in.ArrivalEnd} {
			t, err := authorization.ParseDispatchInstant(r.PostForm.Get(name), r.PostForm.Get(name+"_offset"), form.Zone)
			if err != nil {
				failInput(err)
				return
			}
			*dest = t
		}
		for _, v := range r.PostForm["assignee_ids"] {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				failInput(authorization.ErrDispatchInput)
				return
			}
			in.Assignees = append(in.Assignees, n)
		}
		review, err := req.Service.ReviewDispatch(r.Context(), req.Actor, id, in)
		if err != nil {
			if errors.Is(err, authorization.ErrDispatchInput) {
				failInput(err)
			} else {
				dispatchError(w, r, err)
			}
			return
		}
		render(w, r, templates.JobDispatch(id, review))
		return
	}
	if date := r.URL.Query().Get("date"); date != "" && form.CanSchedule {
		loc, err := time.LoadLocation(form.Zone)
		if err != nil {
			dispatchError(w, r, authorization.ErrDispatchInput)
			return
		}
		clock := "08:00:00"
		duration := time.Hour
		if form.Plan.Start != nil {
			clock = form.Plan.Start.In(loc).Format("15:04:05.999999999")
			if form.Plan.End != nil {
				duration = form.Plan.End.Sub(*form.Plan.Start)
			}
		}
		if hour := r.URL.Query().Get("hour"); hour != "" {
			h, err := strconv.Atoi(hour)
			if err != nil || h < 0 || h > 23 {
				http.Error(w, "Invalid proposed hour", 400)
				return
			}
			clock = fmt.Sprintf("%02d:00:00", h)
		}
		start, err := authorization.ParseDispatchInstant(date+"T"+clock, "", form.Zone)
		if err != nil {
			form.Draft["start"] = date + "T" + clock
			form.Draft["start_offset"] = ""
			form.Error = err.Error()
		} else {
			if form.Plan.Start != nil {
				delta := start.Sub(*form.Plan.Start)
				if form.Plan.ArrivalStart != nil {
					v := form.Plan.ArrivalStart.Add(delta)
					form.Plan.ArrivalStart = &v
				}
				if form.Plan.ArrivalEnd != nil {
					v := form.Plan.ArrivalEnd.Add(delta)
					form.Plan.ArrivalEnd = &v
				}
			}
			end := start.Add(duration)
			form.Plan.Start, form.Plan.End = start, &end
		}
	}
	if uid := r.URL.Query().Get("user_id"); uid != "" && form.CanAssign {
		for i := range form.Choices {
			if fmt.Sprint(form.Choices[i].ID) == uid {
				form.Choices[i].Selected = true
			}
		}
	}
	render(w, r, templates.JobDispatch(id, form))
}

func dispatchError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, authorization.ErrDispatchInput) {
		http.Error(w, err.Error(), 400)
		return
	}
	jobRelationshipError(w, r, err)
}

// Old drag URLs are proposal-only, including requests from stale browser tabs.
func (h *ScheduleHandler) ProposeDispatch(w http.ResponseWriter, r *http.Request) {
	saveaction.ReadOnly(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid dispatch proposal", 400)
		return
	}
	id, err := strconv.ParseInt(r.PostForm.Get("job_id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid Job", 400)
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	if _, err := req.Service.DispatchForm(r.Context(), req.Actor, id); err != nil {
		dispatchError(w, r, err)
		return
	}
	q := url.Values{}
	for _, key := range []string{"date", "hour", "user_id"} {
		if v := r.PostForm.Get(key); v != "" {
			q.Set(key, v)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d/dispatch?%s", id, q.Encode()), 303)
}
