package delivery

import (
	"bytes"
	"crypto/tls"
	"errors"
	"strings"

	gosmtp "github.com/emersion/go-smtp"
)

// SMTPSender performs outbound SMTP delivery using go-smtp.
type SMTPSender struct{}

// Send opens an SMTP connection to destination (host:port), delivers the
// message, and closes the connection.
//
// If tlsCfg is non-nil, STARTTLS is attempted. If the server does not
// advertise STARTTLS the connection falls back to plain SMTP so that
// delivery is never silently dropped due to a missing capability.
//
// Errors are always returned as *DeliveryError. The Temporary field is true
// for 4xx and network errors (retriable) and false for 5xx (permanent).
func (s *SMTPSender) Send(destination, from string, to []string, data []byte, tlsCfg *tls.Config) error {
	c, err := dial(destination, tlsCfg)
	if err != nil {
		return &DeliveryError{Code: 0, Message: err.Error(), Temporary: true}
	}
	defer c.Close()

	if err := c.SendMail(from, to, bytes.NewReader(data)); err != nil {
		return classifySMTPError(err)
	}
	if err := c.Quit(); err != nil {
		// Quit failure after a successful DATA is not worth failing the delivery.
		_ = err
	}
	return nil
}

// dial connects to addr, upgrading to STARTTLS when tlsCfg is provided and
// the server advertises support. Falls back to plain SMTP when the server
// does not advertise STARTTLS so delivery is never silently lost.
func dial(addr string, tlsCfg *tls.Config) (*gosmtp.Client, error) {
	if tlsCfg == nil {
		return gosmtp.Dial(addr)
	}

	c, err := gosmtp.DialStartTLS(addr, tlsCfg)
	if err == nil {
		return c, nil
	}

	// Fall back to plain if the server simply doesn't advertise STARTTLS.
	if strings.Contains(err.Error(), "doesn't support STARTTLS") {
		return gosmtp.Dial(addr)
	}
	return nil, err
}

// classifySMTPError converts a go-smtp error into a *DeliveryError,
// preserving the code and temporary/permanent classification.
func classifySMTPError(err error) *DeliveryError {
	var smtpErr *gosmtp.SMTPError
	if errors.As(err, &smtpErr) {
		return &DeliveryError{
			Code:      smtpErr.Code,
			Message:   smtpErr.Message,
			Temporary: smtpErr.Temporary(),
		}
	}
	// Network or protocol errors are transient.
	return &DeliveryError{Code: 0, Message: err.Error(), Temporary: true}
}
