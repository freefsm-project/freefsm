package delivery

import (
	"context"
	"errors"
	"net"
	"net/textproto"
	"strings"

	"github.com/freefsm-project/freefsm/internal/services"
)

// SMTPSender records only SMTP acceptance. SMTP provides no delivered signal.
type SMTPSender struct{ email *services.EmailService }

func NewSMTPSender(email *services.EmailService) *SMTPSender { return &SMTPSender{email: email} }
func (s *SMTPSender) Send(ctx context.Context, d Delivery) (SendResult, error) {
	err := s.email.SendCompanyDocumentSnapshot(ctx, d.CompanyID, services.EmailRecipients{To: d.To, CC: d.CC, BCC: d.BCC}, d.Subject, d.TextBody, d.HTMLBody, d.MessageID, services.EmailAttachment{Filename: d.PDFFilename, ContentType: "application/pdf", Data: d.PDF})
	if err != nil {
		return SendResult{}, classifySMTPError(err)
	}
	return SendResult{ProviderIdentifier: d.MessageID, Evidence: map[string]any{"transport": "smtp", "result": "accepted", "message_id": d.MessageID}}, nil
}

func classifySMTPError(err error) error {
	var protocol *textproto.Error
	if errors.As(err, &protocol) {
		if protocol.Code >= 500 {
			return &SendError{Kind: SendPermanent, Err: err}
		}
		return &SendError{Kind: SendTemporary, Err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return &SendError{Kind: SendTemporary, Err: err}
	}
	if strings.Contains(err.Error(), "final DATA acceptance") {
		return &SendError{Kind: SendAmbiguous, Err: err}
	}
	if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "not configured") {
		return &SendError{Kind: SendPermanent, Err: err}
	}
	return &SendError{Kind: SendTemporary, Err: err}
}
