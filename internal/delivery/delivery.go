// Package delivery implements store-and-forward outbound SMTP delivery.
package delivery

import (
	"crypto/tls"
	"fmt"

	"github.com/phoenix66/envoy/internal/config"
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
	cfg config.Config
	q   *queue.Queue
}

// New returns a Deliverer backed by the given queue.
func New(cfg config.Config, q *queue.Queue) *Deliverer {
	return &Deliverer{cfg: cfg, q: q}
}

// DeliverForward enqueues msg's raw bytes for relay to the next-hop MTA
// configured for msg.Domain.
func (d *Deliverer) DeliverForward(msg *message.InboundMessage) error {
	nextHop := ""
	for _, dom := range d.cfg.Domains {
		if dom.Name == msg.Domain {
			nextHop = dom.NextHop
			break
		}
	}
	if nextHop == "" {
		return fmt.Errorf("delivery: no next_hop configured for domain %q", msg.Domain)
	}

	entry := queue.QueueEntry{
		ID:           msg.ID,
		Raw:          msg.RawMessage,
		Dest:         nextHop,
		EnvelopeFrom: msg.EnvelopeFrom,
		EnvelopeTo:   msg.EnvelopeTo,
		MsgID:        msg.ID,
	}
	return d.q.Enqueue(queue.BucketForward, entry)
}

// DeliverJournal enqueues the pre-built journal envelope for delivery to the
// configured archive SMTP server.
func (d *Deliverer) DeliverJournal(msg *message.InboundMessage, envelope []byte) error {
	dest := fmt.Sprintf("%s:%d", d.cfg.Archive.SMTPHost, d.cfg.Archive.SMTPPort)

	entry := queue.QueueEntry{
		ID:           msg.ID,
		Raw:          envelope,
		Dest:         dest,
		EnvelopeFrom: d.cfg.Archive.JournalFrom,
		EnvelopeTo:   []string{d.cfg.Archive.JournalTo},
		MsgID:        msg.ID,
	}
	return d.q.Enqueue(queue.BucketJournal, entry)
}
