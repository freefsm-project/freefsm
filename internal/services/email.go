package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type EmailService struct {
	svc *CompanySettingsService
}

func NewEmailService(svc *CompanySettingsService) *EmailService {
	return &EmailService{svc: svc}
}

func (s *EmailService) SendTestEmail(ctx context.Context, to, name string) error {
	cs, err := s.svc.Get(ctx)
	if err != nil || cs == nil || cs.SMTPHost == "" {
		return fmt.Errorf("SMTP not configured")
	}

	subject := "Test Email — FreeFSM"
	body := fmt.Sprintf(`Hi %s,

This is a test email from FreeFSM. Your SMTP settings are working correctly.

If you received this email, your email configuration is ready to use.

- FreeFSM`, name)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		cs.SMTPFrom, to, subject, body)

	addr := fmt.Sprintf("%s:%d", cs.SMTPHost, cs.SMTPPort)

	if cs.SMTPPort == 587 || cs.SMTPPort == 465 {
		return s.sendWithSTARTTLS(addr, cs.SMTPUser, cs.SMTPPassword, cs.SMTPFrom, to, msg)
	}
	return s.sendPlain(addr, cs.SMTPHost, cs.SMTPUser, cs.SMTPPassword, cs.SMTPFrom, to, msg)
}

func (s *EmailService) SendPasswordReset(ctx context.Context, to, name, link string) error {
	cs, err := s.svc.Get(ctx)
	if err != nil || cs == nil || cs.SMTPHost == "" {
		return fmt.Errorf("SMTP not configured")
	}

	subject := "Password Reset - FreeFSM"
	body := fmt.Sprintf(`Hi %s,

A password reset was requested for your account.

Click the link below to reset your password:
%s

This link expires in 1 hour.

If you did not request this, please ignore this email.

- FreeFSM`, name, link)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		cs.SMTPFrom, to, subject, body)

	addr := fmt.Sprintf("%s:%d", cs.SMTPHost, cs.SMTPPort)

	if cs.SMTPPort == 587 || cs.SMTPPort == 465 {
		return s.sendWithSTARTTLS(addr, cs.SMTPUser, cs.SMTPPassword, cs.SMTPFrom, to, msg)
	}
	return s.sendPlain(addr, cs.SMTPHost, cs.SMTPUser, cs.SMTPPassword, cs.SMTPFrom, to, msg)
}

func (s *EmailService) sendWithSTARTTLS(addr, user, password, from, to, msg string) error {
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

	if user != "" {
		auth := smtp.PlainAuth("", user, password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
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

func (s *EmailService) sendPlain(addr, host, user, password, from, to, msg string) error {
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

	if user != "" {
		auth := smtp.PlainAuth("", user, password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
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
