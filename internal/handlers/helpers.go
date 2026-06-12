package handlers

import (
	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/templates"
)

func customerMap(customers []*ent.Customer) map[int64]string {
	m := make(map[int64]string, len(customers))
	for _, c := range customers {
		m[c.ID] = c.DisplayName
	}
	return m
}

func statusOptions(statuses []*ent.Status) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(statuses))
	for i, s := range statuses {
		opts[i] = templates.SelectOption{Value: s.ID, Label: s.Name}
	}
	return opts
}

func customerOptions(customers []*ent.Customer) []templates.SelectOption {
	opts := make([]templates.SelectOption, len(customers))
	for i, c := range customers {
		opts[i] = templates.SelectOption{Value: c.ID, Label: c.DisplayName}
	}
	return opts
}

func statusName(statuses []*ent.Status, id *int64) string {
	if id == nil {
		return ""
	}
	for _, s := range statuses {
		if s.ID == *id {
			return s.Name
		}
	}
	return "Unknown"
}

func int64Ptr(v int64) *int64 {
	if v == 0 {
		return nil
	}
	return &v
}

func formPtr(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
