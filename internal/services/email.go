package services

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
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

	addr := fmt.Sprintf("%s:%d", cs.SMTPHost, cs.SMTPPort)
	if cs.SMTPPort == 465 {
		return s.sendTLS(addr, cs.SMTPUser, cs.SMTPPassword, from, envelopeRecipients, msg)
	}
	if cs.SMTPPort == 587 {
		return s.sendWithSTARTTLS(addr, cs.SMTPUser, cs.SMTPPassword, from, envelopeRecipients, msg)
	}
	return s.sendPlain(addr, cs.SMTPHost, cs.SMTPUser, cs.SMTPPassword, from, envelopeRecipients, msg)
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

func (s *EmailService) sendWithSTARTTLS(addr, user, password, from string, recipients []string, msg string) error {
	host := strings.Split(addr, ":")[0]
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Quit()

	tlsConfig := &tls.Config{ServerName: host}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}

	return sendSMTPMessage(client, host, user, password, from, recipients, msg)
}

func (s *EmailService) sendTLS(addr, user, password, from string, recipients []string, msg string) error {
	host := strings.Split(addr, ":")[0]

	tlsConfig := &tls.Config{ServerName: host}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Quit()

	return sendSMTPMessage(client, host, user, password, from, recipients, msg)
}

func (s *EmailService) sendPlain(addr, host, user, password, from string, recipients []string, msg string) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer client.Quit()

	return sendSMTPMessage(client, host, user, password, from, recipients, msg)
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
	w.Close()

	return nil
}
