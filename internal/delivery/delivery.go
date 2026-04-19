// Package delivery implements store-and-forward outbound SMTP delivery.
package delivery

import (
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/journal"
	"github.com/phoenix66/envoy/internal/message"
	"github.com/phoenix66/envoy/internal/queue"
)

// DeliveryError is returned by Sender.Send when the remote SMTP server
// responds with an error code or the connection fails.
type DeliveryError struct {
	// Code is the SMTP response code, or 0 for network/connection errors.
	Code int
	// Message is the server's human-readable error text.
	Message string
	// Temporary is true for 4xx responses and network errors (worth retrying),
	// false for 5xx responses (permanent failure).
	Temporary bool
}

func (e *DeliveryError) Error() string {
	if e.Code == 0 {
		return fmt.Sprintf("delivery: connection error: %s", e.Message)
	}
	return fmt.Sprintf("delivery: smtp %d: %s", e.Code, e.Message)
}

// Sender is the interface for outbound SMTP delivery.
// It is satisfied by SMTPSender and can be replaced with a mock in tests.
type Sender interface {
	Send(destination, from string, to []string, data []byte, tlsCfg *tls.Config) error
}

// Deliverer enqueues messages into the persistent queue for store-and-forward
// delivery. It does not perform SMTP itself; that is the Worker's job.
type Deliverer struct {
	cfg      config.Config
	q        *queue.Queue
	builders []*journal.Builder // one per archive target, index-aligned with cfg.Archive.Targets
}

// New returns a Deliverer backed by the given queue. A journal.Builder is
// created for each configured archive target.
func New(cfg config.Config, q *queue.Queue) *Deliverer {
	builders := make([]*journal.Builder, len(cfg.Archive.Targets))
	for i, t := range cfg.Archive.Targets {
		builders[i] = journal.New(t)
	}
	return &Deliverer{cfg: cfg, q: q, builders: builders}
}

// DeliverJournal builds a journal envelope for each configured archive target
// and enqueues an independent copy into that target's named bucket.
// Each enqueue is independent: a failure for one target does not prevent
// delivery to other targets. All errors are collected and returned together.
func (d *Deliverer) DeliverJournal(msg *message.InboundMessage) error {
	var errs []error
	for i, target := range d.cfg.Archive.Targets {
		envelope, err := d.builders[i].Build(msg)
		if err != nil {
			errs = append(errs, fmt.Errorf("archive target %q: building journal envelope: %w", target.Name, err))
			continue
		}

		dest := fmt.Sprintf("%s:%d", target.SMTPHost, target.SMTPPort)
		entryID := msg.ID + "." + target.Name
		entry := queue.QueueEntry{
			ID:           entryID,
			Raw:          envelope,
			Dest:         dest,
			EnvelopeFrom: target.JournalFrom,
			EnvelopeTo:   []string{target.JournalTo},
			MsgID:        msg.ID,
		}
		if err := d.q.Enqueue("archive."+target.Name, entry); err != nil {
			errs = append(errs, fmt.Errorf("archive target %q: enqueuing: %w", target.Name, err))
		}
	}
	return errors.Join(errs...)
}

// DeliverForward enqueues the original message verbatim for each configured
// forward target into that target's named bucket. The target's From address
// is used as the MAIL FROM value; the original EnvelopeTo recipients are used
// as RCPT TO. Each enqueue is independent: a failure for one target does not
// prevent delivery to other targets. All errors are collected and returned
// together.
func (d *Deliverer) DeliverForward(msg *message.InboundMessage) error {
	var errs []error
	for _, target := range d.cfg.Forward.Targets {
		dest := fmt.Sprintf("%s:%d", target.SMTPHost, target.SMTPPort)
		entryID := msg.ID + "." + target.Name
		entry := queue.QueueEntry{
			ID:           entryID,
			Raw:          msg.RawMessage,
			Dest:         dest,
			EnvelopeFrom: target.From,
			EnvelopeTo:   msg.EnvelopeTo,
			MsgID:        msg.ID,
		}
		if err := d.q.Enqueue("forward."+target.Name, entry); err != nil {
			errs = append(errs, fmt.Errorf("forward target %q: enqueuing: %w", target.Name, err))
		}
	}
	return errors.Join(errs...)
}
