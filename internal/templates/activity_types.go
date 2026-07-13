package templates

import (
	"fmt"
	"strings"
)

type ActivityEntry struct {
	ID         int64
	ActorName  string
	Action     string
	TargetType string
	EntityName string
	EntityURL  string
	Icon       string
	Metadata   ActivityMetadata
	CreatedAt  string
}

type ActivityMetadata struct {
	EntityName      string `json:"entity_name,omitempty"`
	ActorName       string `json:"actor_name,omitempty"`
	TagName         string `json:"tag_name,omitempty"`
	FileName        string `json:"file_name,omitempty"`
	CommentPreview  string `json:"comment_preview,omitempty"`
	OldStatus       string `json:"old_status,omitempty"`
	NewStatus       string `json:"new_status,omitempty"`
	Amount          string `json:"amount,omitempty"`
	Changed         string `json:"changed,omitempty"`
	OldStart        string `json:"old_start,omitempty"`
	NewStart        string `json:"new_start,omitempty"`
	OldEnd          string `json:"old_end,omitempty"`
	NewEnd          string `json:"new_end,omitempty"`
	OldStartDisplay string `json:"old_start_display,omitempty"`
	NewStartDisplay string `json:"new_start_display,omitempty"`
	OldEndDisplay   string `json:"old_end_display,omitempty"`
	NewEndDisplay   string `json:"new_end_display,omitempty"`
	OldAssignee     string `json:"old_assignee,omitempty"`
	NewAssignee     string `json:"new_assignee,omitempty"`
	Source          string `json:"source,omitempty"`
}

type ActivityWidgetData struct {
	DOMID   string
	Entries []ActivityEntry
}

type ActivityPageData struct {
	Entries    []ActivityEntry
	Page       int
	PerPage    int
	Total      int
	TotalPages int
}

func activityVerb(action string) string {
	switch action {
	case "created", "type_created", "status_created", "field_created", "tag_created", "user_created", "contact_created", "location_created":
		return "created"
	case "updated", "type_updated", "status_updated", "field_updated", "tag_updated", "user_updated", "contact_updated", "location_updated":
		return "updated"
	case "deleted", "type_deleted", "status_deleted", "field_deleted", "tag_deleted", "contact_deleted", "location_deleted":
		return "deleted"
	case "archived":
		return "archived"
	case "settings_updated":
		return "updated"
	case "status_changed":
		return "changed"
	case "scheduled":
		return "scheduled"
	case "rescheduled":
		return "rescheduled"
	case "dispatched":
		return "dispatched"
	case "tag_attached":
		return "attached"
	case "tag_detached":
		return "detached"
	case "file_uploaded":
		return "uploaded"
	case "file_deleted":
		return "deleted"
	case "comment_added":
		return "added"
	case "comment_deleted":
		return "deleted"
	case "clocked_in":
		return "clocked in"
	case "clocked_out":
		return "clocked out"
	case "payment_recorded":
		return "recorded"
	case "payment_deleted":
		return "deleted"
	case "converted":
		return "converted"
	case "user_disabled":
		return "disabled"
	case "user_enabled":
		return "enabled"
	case "password_reset":
		return "reset password of"
	case "password_changed":
		return "changed password"
	case "welcome_resent":
		return "resent welcome to"
	case "subtask_completed":
		return "completed a subtask on"
	case "subtask_uncompleted":
		return "uncompleted a subtask on"
	case "logged_in":
		return "logged in"
	case "logged_out":
		return "logged out"
	case "logo_uploaded":
		return "updated logo for"
	case "restored":
		return "restored"
	default:
		return action
	}
}

func activityActionClass(action string) string {
	switch action {
	case "created", "type_created", "status_created", "field_created", "tag_created", "user_created", "contact_created", "location_created":
		return "activity-created"
	case "updated", "type_updated", "status_updated", "field_updated", "tag_updated", "user_updated", "contact_updated", "location_updated", "settings_updated":
		return "activity-updated"
	case "deleted", "type_deleted", "status_deleted", "field_deleted", "tag_deleted", "contact_deleted", "location_deleted":
		return "activity-deleted"
	case "archived":
		return "activity-deleted"
	case "status_changed":
		return "activity-status"
	case "scheduled", "rescheduled", "dispatched":
		return "activity-schedule"
	case "tag_attached", "tag_detached":
		return "activity-tag"
	case "file_uploaded", "file_deleted":
		return "activity-file"
	case "comment_added", "comment_deleted":
		return "activity-comment"
	case "clocked_in", "clocked_out":
		return "activity-timesheet"
	case "payment_recorded", "payment_deleted":
		return "activity-payment"
	case "user_disabled":
		return "activity-user"
	case "user_enabled":
		return "activity-created"
	case "password_reset", "password_changed":
		return "activity-security"
	case "welcome_resent":
		return "activity-email"
	case "subtask_completed", "subtask_uncompleted":
		return "activity-subtask"
	case "logged_in":
		return "activity-created"
	case "logged_out":
		return "activity-deleted"
	case "logo_uploaded":
		return "activity-file"
	case "restored":
		return "activity-created"
	default:
		return ""
	}
}

// TruncateText truncates a string to the given max rune count, adding "..." if truncated.
func TruncateText(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func ScheduleActivityDetail(meta ActivityMetadata) string {
	parts := []string{}
	oldWindow := scheduleWindow(meta.OldStartDisplay, meta.OldEndDisplay)
	newWindow := scheduleWindow(meta.NewStartDisplay, meta.NewEndDisplay)
	if oldWindow != "" && newWindow != "" && oldWindow != newWindow {
		parts = append(parts, oldWindow+" -> "+newWindow)
	} else if newWindow != "" {
		parts = append(parts, newWindow)
	} else if oldWindow != "" {
		parts = append(parts, oldWindow)
	}

	if meta.OldAssignee != "" && meta.NewAssignee != "" && meta.OldAssignee != meta.NewAssignee {
		parts = append(parts, meta.OldAssignee+" -> "+meta.NewAssignee)
	} else if meta.NewAssignee != "" {
		parts = append(parts, "assigned to "+meta.NewAssignee)
	} else if meta.OldAssignee != "" {
		parts = append(parts, "assigned to "+meta.OldAssignee)
	}

	if meta.Source != "" {
		parts = append(parts, "source: "+meta.Source)
	}

	return strings.Join(parts, "; ")
}

func scheduleWindow(start, end string) string {
	if start == "" {
		return ""
	}
	if end == "" {
		return start
	}
	return start + "-" + end
}

func sprintHTML(vals ...interface{}) string {
	var sb strings.Builder
	for _, v := range vals {
		fmt.Fprint(&sb, v)
	}
	return sb.String()
}
