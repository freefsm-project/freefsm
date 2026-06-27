package services

import (
	"strings"
	"testing"
)

func TestParseEmailRecipients(t *testing.T) {
	recipients, err := ParseEmailRecipients("one@example.com, Two <two@example.com>", "cc@example.com", "bcc@example.com")
	if err != nil {
		t.Fatalf("ParseEmailRecipients: %v", err)
	}
	if got, want := strings.Join(recipients.To, ","), "one@example.com,two@example.com"; got != want {
		t.Fatalf("To = %q, want %q", got, want)
	}
	if got, want := strings.Join(recipients.CC, ","), "cc@example.com"; got != want {
		t.Fatalf("CC = %q, want %q", got, want)
	}
	if got, want := strings.Join(recipients.BCC, ","), "bcc@example.com"; got != want {
		t.Fatalf("BCC = %q, want %q", got, want)
	}
}

func TestParseEmailRecipientsRejectsEmptyTo(t *testing.T) {
	if _, err := ParseEmailRecipients("", "cc@example.com", ""); err == nil {
		t.Fatal("ParseEmailRecipients error = nil, want empty To error")
	}
}

func TestParseEmailListUsesCommaOnly(t *testing.T) {
	if _, err := ParseEmailList("one@example.com;two@example.com"); err == nil {
		t.Fatal("ParseEmailList semicolon error = nil, want invalid address")
	}
}

func TestBuildEmailMessageHeaders(t *testing.T) {
	recipients := EmailRecipients{
		To:  []string{"to1@example.com", "to2@example.com"},
		CC:  []string{"cc@example.com"},
		BCC: []string{"bcc@example.com"},
	}
	msg, err := buildEmailMessage("from@example.com", recipients, "Subject", "Body", nil)
	if err != nil {
		t.Fatalf("buildEmailMessage: %v", err)
	}
	if !strings.Contains(msg, "To: to1@example.com, to2@example.com\r\n") {
		t.Fatalf("missing To header: %q", msg)
	}
	if !strings.Contains(msg, "Cc: cc@example.com\r\n") {
		t.Fatalf("missing Cc header: %q", msg)
	}
	if strings.Contains(strings.ToLower(msg), "bcc") {
		t.Fatalf("message contains Bcc data: %q", msg)
	}
}

func TestEnvelopeRecipientsDedupesAcrossGroups(t *testing.T) {
	recipients := EmailRecipients{
		To:  []string{"to@example.com", "shared@example.com"},
		CC:  []string{"shared@example.com"},
		BCC: []string{"TO@example.com", "hidden@example.com"},
	}
	got := strings.Join(recipients.EnvelopeRecipients(), ",")
	want := "to@example.com,shared@example.com,hidden@example.com"
	if got != want {
		t.Fatalf("EnvelopeRecipients = %q, want %q", got, want)
	}
}
