package services

import "encoding/json"

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

type JobSubtask struct {
	Title     string `json:"title"`
	Completed bool   `json:"completed"`
	SortOrder int    `json:"sort_order"`
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
