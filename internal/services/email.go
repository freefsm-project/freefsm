package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
)

type EmailService struct {
	svc *CompanySettingsService
}

type EmailAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type EmailRecipients struct {
	To  []string
	CC  []string
	BCC []string
}

func NewEmailService(svc *CompanySettingsService) *EmailService {
	return &EmailService{svc: svc}
}

func (s *EmailService) SendTestEmail(ctx context.Context, to, name string) error {
	subject := "Test Email — FreeFSM"
	body := fmt.Sprintf(`Hi %s,

This is a test email from FreeFSM. Your SMTP settings are working correctly.

If you received this email, your email configuration is ready to use.

- FreeFSM`, name)

	return s.SendEmail(ctx, to, subject, body)
}

func (s *EmailService) SendWelcomeEmail(ctx context.Context, to, name, inviteURL string) error {
	subject := "Welcome to FreeFSM"
	body := fmt.Sprintf(`Hi %s,

Your FreeFSM account has been created.

Email: %s

Click the link below to set your password and activate your account:
%s

This invitation expires in 72 hours.

- FreeFSM`, name, to, inviteURL)

	return s.SendEmail(ctx, to, subject, body)
}

func (s *EmailService) SendPasswordReset(ctx context.Context, to, name, link string) error {
	subject := "Password Reset - FreeFSM"
	body := fmt.Sprintf(`Hi %s,

A password reset was requested for your account.

Click the link below to reset your password:
%s

This link expires in 1 hour.

If you did not request this, please ignore this email.

- FreeFSM`, name, link)

	return s.SendEmail(ctx, to, subject, body)
}

func (s *EmailService) SendEmailWithAttachment(ctx context.Context, to, subject, body, filename, mimeType string, data []byte) error {
	return s.SendEmail(ctx, to, subject, body, EmailAttachment{
		Filename:    filename,
		ContentType: mimeType,
		Data:        data,
	})
}

func (s *EmailService) SendEmail(ctx context.Context, to, subject, body string, attachments ...EmailAttachment) error {
	recipients, err := ParseEmailRecipients(to, "", "")
	if err != nil {
		return err
	}
	return s.SendEmailTo(ctx, recipients, subject, body, attachments...)
}

func (s *EmailService) SendEmailWithAttachmentTo(ctx context.Context, recipients EmailRecipients, subject, body, filename, mimeType string, data []byte) error {
	return s.SendEmailTo(ctx, recipients, subject, body, EmailAttachment{
		Filename:    filename,
		ContentType: mimeType,
		Data:        data,
	})
}

func (s *EmailService) SendEmailTo(ctx context.Context, recipients EmailRecipients, subject, body string, attachments ...EmailAttachment) error {
	cs, err := s.svc.Get(ctx)
	if err != nil || cs == nil || cs.SMTPHost == "" {
		return fmt.Errorf("SMTP not configured")
	}

	from := sanitizeHeader(cs.SMTPFrom)
	if len(recipients.To) == 0 {
		return fmt.Errorf("recipient email is required")
	}

	msg, err := buildEmailMessage(from, recipients, subject, body, attachments)
	if err != nil {
		return err
	}
	envelopeRecipients := recipients.EnvelopeRecipients()

	return s.sendBuilt(ctx, cs, from, envelopeRecipients, msg)
}

// SendDocumentSnapshot sends a fully rendered immutable outbox snapshot.
func (s *EmailService) SendDocumentSnapshot(ctx context.Context, recipients EmailRecipients, subject, textBody, htmlBody, messageID string, attachment EmailAttachment) error {
	cs, err := s.svc.Get(ctx)
	if err != nil || cs == nil || cs.SMTPHost == "" {
		return fmt.Errorf("SMTP not configured")
	}
	from := sanitizeHeader(cs.SMTPFrom)
	msg, err := buildDocumentEmailMessage(from, recipients, subject, textBody, htmlBody, messageID, attachment)
	if err != nil {
		return err
	}
	return s.sendBuilt(ctx, cs, from, recipients.EnvelopeRecipients(), msg)
}

func (s *EmailService) SendCompanyDocumentSnapshot(ctx context.Context, companyID int64, recipients EmailRecipients, subject, textBody, htmlBody, messageID string, attachment EmailAttachment) error {
	cs, err := s.svc.GetForCompany(ctx, companyID)
	if err != nil || cs == nil || cs.SMTPHost == "" {
		return fmt.Errorf("SMTP not configured for company %d", companyID)
	}
	from := sanitizeHeader(cs.SMTPFrom)
	msg, err := buildDocumentEmailMessage(from, recipients, subject, textBody, htmlBody, messageID, attachment)
	if err != nil {
		return err
	}
	return s.sendBuilt(ctx, cs, from, recipients.EnvelopeRecipients(), msg)
}

func (s *EmailService) sendBuilt(ctx context.Context, cs *ent.CompanySettings, from string, envelopeRecipients []string, msg string) error {
	addr := fmt.Sprintf("%s:%d", cs.SMTPHost, cs.SMTPPort)
	if cs.SMTPPort == 465 {
		return s.sendTLS(ctx, addr, cs.SMTPUser, cs.SMTPPassword, from, envelopeRecipients, msg)
	}
	if cs.SMTPPort == 587 {
		return s.sendWithSTARTTLS(ctx, addr, cs.SMTPUser, cs.SMTPPassword, from, envelopeRecipients, msg)
	}
	return s.sendPlain(ctx, addr, cs.SMTPHost, cs.SMTPUser, cs.SMTPPassword, from, envelopeRecipients, msg)
}

func buildDocumentEmailMessage(from string, recipients EmailRecipients, subject, textBody, htmlBody, messageID string, attachment EmailAttachment) (string, error) {
	if len(recipients.To) == 0 || sanitizeHeader(messageID) == "" {
		return "", fmt.Errorf("recipient and message ID are required")
	}
	sum := sha256.Sum256([]byte(messageID))
	mixed := fmt.Sprintf("freefsm-mixed-%x", sum[:12])
	alt := fmt.Sprintf("freefsm-alt-%x", sum[12:24])
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\nTo: %s\r\n", sanitizeHeader(from), sanitizeHeader(strings.Join(recipients.To, ", ")))
	if len(recipients.CC) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", sanitizeHeader(strings.Join(recipients.CC, ", ")))
	}
	fmt.Fprintf(&buf, "Subject: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n", mime.QEncoding.Encode("UTF-8", sanitizeHeader(subject)), sanitizeHeader(messageID), mixed)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", mixed, alt)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", alt, textBody)
	if htmlBody != "" {
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", alt, htmlBody)
	}
	fmt.Fprintf(&buf, "--%s--\r\n", alt)
	filename := sanitizeHeader(attachment.Filename)
	if filename == "" {
		return "", fmt.Errorf("attachment filename is required")
	}
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: application/pdf\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: %s\r\n\r\n", mixed, mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	writeBase64Lines(&buf, attachment.Data)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", mixed)
	return buf.String(), nil
}

func ParseEmailRecipients(to, cc, bcc string) (EmailRecipients, error) {
	recipients := EmailRecipients{}
	var err error
	if recipients.To, err = ParseEmailList(to); err != nil {
		return recipients, fmt.Errorf("to: %w", err)
	}
	if len(recipients.To) == 0 {
		return recipients, fmt.Errorf("recipient email is required")
	}
	if recipients.CC, err = ParseEmailList(cc); err != nil {
		return recipients, fmt.Errorf("cc: %w", err)
	}
	if recipients.BCC, err = ParseEmailList(bcc); err != nil {
		return recipients, fmt.Errorf("bcc: %w", err)
	}
	return recipients, nil
}

func ParseEmailList(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	addresses := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		addr, err := mail.ParseAddress(part)
		if err != nil {
			return nil, fmt.Errorf("invalid email address %q", part)
		}
		addresses = append(addresses, addr.Address)
	}
	return addresses, nil
}

func (r EmailRecipients) EnvelopeRecipients() []string {
	seen := map[string]bool{}
	all := make([]string, 0, len(r.To)+len(r.CC)+len(r.BCC))
	for _, group := range [][]string{r.To, r.CC, r.BCC} {
		for _, addr := range group {
			key := strings.ToLower(addr)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, addr)
		}
	}
	return all
}

func RenderEmailTemplate(template string, values map[string]string) string {
	for key, value := range values {
		template = strings.ReplaceAll(template, "{"+key+"}", value)
	}
	return template
}

func buildEmailMessage(from string, recipients EmailRecipients, subject, body string, attachments []EmailAttachment) (string, error) {
	from = sanitizeHeader(from)
	subject = sanitizeHeader(subject)
	toHeader := sanitizeHeader(strings.Join(recipients.To, ", "))
	ccHeader := sanitizeHeader(strings.Join(recipients.CC, ", "))
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\n", from, toHeader)
	if ccHeader != "" {
		headers += fmt.Sprintf("Cc: %s\r\n", ccHeader)
	}
	headers += fmt.Sprintf("Subject: %s\r\nMIME-Version: 1.0\r\n", mime.QEncoding.Encode("UTF-8", subject))

	if len(attachments) == 0 {
		return fmt.Sprintf("%sContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", headers, body), nil
	}

	boundary := fmt.Sprintf("freefsm-%d", time.Now().UnixNano())
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "%sContent-Type: multipart/mixed; boundary=%q\r\n\r\n", headers, boundary)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, body)

	for _, attachment := range attachments {
		filename := sanitizeHeader(attachment.Filename)
		if filename == "" {
			return "", fmt.Errorf("attachment filename is required")
		}
		contentType := sanitizeHeader(attachment.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
		fmt.Fprintf(&buf, "\r\n--%s\r\nContent-Type: %s\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: %s\r\n\r\n", boundary, contentType, disposition)
		writeBase64Lines(&buf, attachment.Data)
		buf.WriteString("\r\n")
	}
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	return buf.String(), nil
}

func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "")
}

func writeBase64Lines(buf *bytes.Buffer, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for len(encoded) > 76 {
		buf.WriteString(encoded[:76])
		buf.WriteString("\r\n")
		encoded = encoded[76:]
	}
	buf.WriteString(encoded)
}

func (s *EmailService) sendWithSTARTTLS(ctx context.Context, addr, user, password, from string, recipients []string, msg string) error {
	host := strings.Split(addr, ":")[0]
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	stop := closeOnCancel(ctx, conn)
	defer stop()
	setContextDeadline(ctx, conn)

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Close()

	tlsConfig := &tls.Config{ServerName: host}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	} else {
		return fmt.Errorf("STARTTLS required but server does not advertise it")
	}

	return sendSMTPMessage(client, host, user, password, from, recipients, msg)
}

func (s *EmailService) sendTLS(ctx context.Context, addr, user, password, from string, recipients []string, msg string) error {
	host := strings.Split(addr, ":")[0]

	tlsConfig := &tls.Config{ServerName: host}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("TLS connect: %w", err)
	}
	conn := tls.Client(raw, tlsConfig)
	defer conn.Close()
	stop := closeOnCancel(ctx, conn)
	defer stop()
	setContextDeadline(ctx, conn)
	if err := conn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TLS handshake: %w", err)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Close()

	return sendSMTPMessage(client, host, user, password, from, recipients, msg)
}

func (s *EmailService) sendPlain(ctx context.Context, addr, host, user, password, from string, recipients []string, msg string) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	stop := closeOnCancel(ctx, conn)
	defer stop()
	setContextDeadline(ctx, conn)

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Close()

	return sendSMTPMessage(client, host, user, password, from, recipients, msg)
}

func setContextDeadline(ctx context.Context, conn net.Conn) {
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
}

func closeOnCancel(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func sendSMTPMessage(client *smtp.Client, host, user, password, from string, recipients []string, msg string) error {
	if user != "" {
		auth := smtp.PlainAuth("", user, password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", recipient, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	_, err = w.Write([]byte(msg))
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("final DATA acceptance: %w", err)
	}
	return nil
}
