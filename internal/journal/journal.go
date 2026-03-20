// Package journal constructs Exchange-style journal report envelopes
// suitable for delivery to MailArchiva or compatible archiving systems.
package journal

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	gomail "github.com/emersion/go-message"
	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/message"
)

// Builder constructs journal report envelopes from received messages.
type Builder struct {
	cfg config.ArchiveConfig
}

// New returns a Builder configured with the provided archive settings.
func New(cfg config.ArchiveConfig) *Builder {
	return &Builder{cfg: cfg}
}

// Build constructs a complete RFC 5322 journal report envelope for msg.
// The returned bytes are ready for submission to the archive SMTP server.
func (b *Builder) Build(msg *message.InboundMessage) ([]byte, error) {
	// Extract headers from the original message for the human-readable part.
	origHeaders, err := extractHeaders(msg.RawMessage)
	if err != nil {
		return nil, fmt.Errorf("journal: extracting original headers: %w", err)
	}

	var buf bytes.Buffer

	// --- Outer envelope header ---
	var outerHeader gomail.Header
	outerHeader.Set("From", b.cfg.JournalFrom)
	outerHeader.Set("To", b.cfg.JournalTo)
	outerHeader.Set("Subject", "Journal Report")
	outerHeader.Set("Date", msg.ReceivedAt.UTC().Format(time.RFC1123Z))
	outerHeader.Set("X-Envoy-ID", msg.ID)
	outerHeader.Set("X-Envoy-Direction", string(msg.Direction))
	outerHeader.SetContentType("multipart/report", map[string]string{
		"report-type": "delivery-status",
	})

	mw, err := gomail.CreateWriter(&buf, outerHeader)
	if err != nil {
		return nil, fmt.Errorf("journal: creating outer writer: %w", err)
	}

	// --- Part 1: human-readable journal record (text/plain) ---
	if err := writeTextPart(mw, msg, origHeaders); err != nil {
		return nil, fmt.Errorf("journal: writing text part: %w", err)
	}

	// --- Part 2: original message verbatim (message/rfc822) ---
	if err := writeRFC822Part(mw, msg.RawMessage); err != nil {
		return nil, fmt.Errorf("journal: writing rfc822 part: %w", err)
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("journal: closing outer writer: %w", err)
	}

	return buf.Bytes(), nil
}

// writeTextPart writes the human-readable journal record as a text/plain part.
func writeTextPart(mw *gomail.Writer, msg *message.InboundMessage, orig map[string]string) error {
	var h gomail.Header
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})

	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}

	body := fmt.Sprintf(
		"Sender:           %s\r\n"+
			"Recipients:       %s\r\n"+
			"Subject:          %s\r\n"+
			"Message-Id:       %s\r\n"+
			"Date:             %s\r\n"+
			"Direction:        %s\r\n"+
			"Journal-Record-Id: %s\r\n",
		msg.EnvelopeFrom,
		strings.Join(msg.EnvelopeTo, "; "),
		orig["Subject"],
		orig["Message-Id"],
		orig["Date"],
		string(msg.Direction),
		msg.ID,
	)

	if _, err := fmt.Fprint(pw, body); err != nil {
		return err
	}
	return pw.Close()
}

// writeRFC822Part writes the original message bytes verbatim as a message/rfc822 part.
func writeRFC822Part(mw *gomail.Writer, raw []byte) error {
	var h gomail.Header
	h.SetContentType("message/rfc822", nil)

	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}

	if _, err := pw.Write(raw); err != nil {
		return err
	}
	return pw.Close()
}

// extractHeaders parses raw RFC 5322 bytes and returns a map of the header
// fields relevant to the journal record: Subject, Message-Id, and Date.
// Missing fields are returned as empty strings; parse errors are non-fatal
// for individual fields.
func extractHeaders(raw []byte) (map[string]string, error) {
	out := map[string]string{
		"Subject":    "",
		"Message-Id": "",
		"Date":       "",
	}

	entity, err := gomail.Read(bytes.NewReader(raw))
	if err != nil && !gomail.IsUnknownCharset(err) && !gomail.IsUnknownEncoding(err) {
		return out, err
	}

	for k := range out {
		out[k] = entity.Header.Get(k)
	}

	return out, nil
}
