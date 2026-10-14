package services

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

type JobVisit struct {
	Date      string `json:"date"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
	Notes     string `json:"notes"`
}

type JobAssignment struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

func ParseVisits(s string) []JobVisit {
	if s == "" || s == "[]" {
		return nil
	}
	var v []JobVisit
	json.Unmarshal([]byte(s), &v)
	return v
}

func SerializeVisits(visits []JobVisit) string {
	if len(visits) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(visits)
	return string(b)
}

func ParseAssignments(s string) []JobAssignment {
	if s == "" || s == "[]" {
		return nil
	}
	var a []JobAssignment
	json.Unmarshal([]byte(s), &a)
	return a
}

func SerializeAssignments(assignments []JobAssignment) string {
	if len(assignments) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(assignments)
	return string(b)
}

type JobSubtask = authorization.Subtask

// DecodeSubtasks is the write boundary: malformed input must not become an empty
// checklist (which would silently remove every existing entry).
func DecodeSubtasks(s string) ([]JobSubtask, error) {
	if s == "" {
		return nil, nil
	}
	var tasks []JobSubtask
	if err := json.Unmarshal([]byte(s), &tasks); err != nil || tasks == nil {
		return nil, authorization.ErrSubtaskInvalid
	}
	return tasks, nil
}

func ParseSubtasks(s string) []JobSubtask {
	if s == "" || s == "[]" {
		return nil
	}
	var t []JobSubtask
	json.Unmarshal([]byte(s), &t)
	return t
}

func SerializeSubtasks(subtasks []JobSubtask) string {
	if len(subtasks) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(subtasks)
	return string(b)
}
