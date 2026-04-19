package delivery

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/message"
	"github.com/phoenix66/envoy/internal/queue"
)

// openDeliveryQueue creates a queue with the given bucket names initialised.
func openDeliveryQueue(t *testing.T, buckets []string) *queue.Queue {
	t.Helper()
	q, err := queue.Open(filepath.Join(t.TempDir(), "queue.db"), 10)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	if err := q.InitBuckets(buckets); err != nil {
		t.Fatalf("queue.InitBuckets: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// countBucketD counts keys in a named bucket using the db exposed via the
// queue; we access it via Next polling to stay within the public API.
func hasEntry(t *testing.T, q *queue.Queue, bucket string) bool {
	t.Helper()
	entry, err := q.Next(bucket)
	if err != nil {
		t.Fatalf("Next(%q): %v", bucket, err)
	}
	return entry != nil
}

// testMessage returns a minimal InboundMessage for use in delivery tests.
func testMessage() *message.InboundMessage {
	return &message.InboundMessage{
		ID:           "ENV-20240101T100000-deadbeef01234567",
		ReceivedAt:   time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		Direction:    message.DirectionInbound,
		EnvelopeFrom: "sender@example.com",
		EnvelopeTo:   []string{"recip@example.com"},
		Domain:       "example.com",
		RawMessage:   []byte("From: sender@example.com\r\nTo: recip@example.com\r\nSubject: Test\r\n\r\nBody.\r\n"),
	}
}

// validArchiveTarget returns a fully populated archive target for testing.
func validArchiveTarget(name string) config.ArchiveTargetConfig {
	return config.ArchiveTargetConfig{
		Name:        name,
		SMTPHost:    "archive.example.com",
		SMTPPort:    7700,
		JournalFrom: "journal@example.com",
		JournalTo:   "archive@example.com",
	}
}

// validForwardTarget returns a fully populated forward target for testing.
func validForwardTarget(name string) config.ForwardTargetConfig {
	return config.ForwardTargetConfig{
		Name:     name,
		SMTPHost: "relay.example.com",
		SMTPPort: 25,
		From:     "relay@example.com",
	}
}

// --- DeliverJournal tests ---

func TestDeliverJournal_SingleTarget_EnqueuesEntry(t *testing.T) {
	buckets := []string{"archive.primary"}
	q := openDeliveryQueue(t, buckets)

	cfg := config.Config{
		Archive: config.ArchiveConfig{
			Targets: []config.ArchiveTargetConfig{validArchiveTarget("primary")},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverJournal(msg); err != nil {
		t.Fatalf("DeliverJournal: %v", err)
	}

	if !hasEntry(t, q, "archive.primary") {
		t.Error("archive.primary bucket has no entry after DeliverJournal")
	}
}

func TestDeliverJournal_MultipleTargets_EnqueuesOnePerTarget(t *testing.T) {
	buckets := []string{"archive.primary", "archive.secondary"}
	q := openDeliveryQueue(t, buckets)

	cfg := config.Config{
		Archive: config.ArchiveConfig{
			Targets: []config.ArchiveTargetConfig{
				validArchiveTarget("primary"),
				validArchiveTarget("secondary"),
			},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverJournal(msg); err != nil {
		t.Fatalf("DeliverJournal: %v", err)
	}

	for _, bucket := range buckets {
		if !hasEntry(t, q, bucket) {
			t.Errorf("bucket %q has no entry after DeliverJournal with two targets", bucket)
		}
	}
}

func TestDeliverJournal_NoTargets_NoError(t *testing.T) {
	q := openDeliveryQueue(t, nil)
	cfg := config.Config{Archive: config.ArchiveConfig{Targets: nil}}
	dlv := New(cfg, q)

	if err := dlv.DeliverJournal(testMessage()); err != nil {
		t.Fatalf("DeliverJournal with no targets: %v", err)
	}
}

func TestDeliverJournal_EnvelopeFields(t *testing.T) {
	target := validArchiveTarget("primary")
	target.JournalFrom = "j@archive.example.com"
	target.JournalTo = "to@archive.example.com"

	buckets := []string{"archive.primary"}
	q := openDeliveryQueue(t, buckets)
	cfg := config.Config{
		Archive: config.ArchiveConfig{Targets: []config.ArchiveTargetConfig{target}},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverJournal(msg); err != nil {
		t.Fatalf("DeliverJournal: %v", err)
	}

	entry, err := q.Next("archive.primary")
	if err != nil || entry == nil {
		t.Fatalf("expected entry in archive.primary bucket, err=%v entry=%v", err, entry)
	}
	if entry.EnvelopeFrom != target.JournalFrom {
		t.Errorf("EnvelopeFrom = %q, want %q", entry.EnvelopeFrom, target.JournalFrom)
	}
	if len(entry.EnvelopeTo) != 1 || entry.EnvelopeTo[0] != target.JournalTo {
		t.Errorf("EnvelopeTo = %v, want [%q]", entry.EnvelopeTo, target.JournalTo)
	}
	expectedDest := "archive.example.com:7700"
	if entry.Dest != expectedDest {
		t.Errorf("Dest = %q, want %q", entry.Dest, expectedDest)
	}
}

func TestDeliverJournal_UniqueEntryIDs(t *testing.T) {
	buckets := []string{"archive.primary", "archive.secondary"}
	q := openDeliveryQueue(t, buckets)

	cfg := config.Config{
		Archive: config.ArchiveConfig{
			Targets: []config.ArchiveTargetConfig{
				validArchiveTarget("primary"),
				validArchiveTarget("secondary"),
			},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverJournal(msg); err != nil {
		t.Fatalf("DeliverJournal: %v", err)
	}

	e1, _ := q.Next("archive.primary")
	e2, _ := q.Next("archive.secondary")
	if e1 == nil || e2 == nil {
		t.Fatal("expected entries in both archive buckets")
	}
	if e1.ID == e2.ID {
		t.Errorf("entry IDs are identical (%q); each target must get a unique ID", e1.ID)
	}
}

// --- DeliverForward tests ---

func TestDeliverForward_SingleTarget_EnqueuesEntry(t *testing.T) {
	buckets := []string{"forward.crm"}
	q := openDeliveryQueue(t, buckets)

	cfg := config.Config{
		Forward: config.ForwardConfig{
			Targets: []config.ForwardTargetConfig{validForwardTarget("crm")},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverForward(msg); err != nil {
		t.Fatalf("DeliverForward: %v", err)
	}

	if !hasEntry(t, q, "forward.crm") {
		t.Error("forward.crm bucket has no entry after DeliverForward")
	}
}

func TestDeliverForward_MultipleTargets_EnqueuesOnePerTarget(t *testing.T) {
	buckets := []string{"forward.crm", "forward.erp"}
	q := openDeliveryQueue(t, buckets)

	cfg := config.Config{
		Forward: config.ForwardConfig{
			Targets: []config.ForwardTargetConfig{
				validForwardTarget("crm"),
				validForwardTarget("erp"),
			},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverForward(msg); err != nil {
		t.Fatalf("DeliverForward: %v", err)
	}

	for _, bucket := range buckets {
		if !hasEntry(t, q, bucket) {
			t.Errorf("bucket %q has no entry after DeliverForward with two targets", bucket)
		}
	}
}

func TestDeliverForward_NoTargets_NoError(t *testing.T) {
	q := openDeliveryQueue(t, nil)
	cfg := config.Config{Forward: config.ForwardConfig{Targets: nil}}
	dlv := New(cfg, q)

	if err := dlv.DeliverForward(testMessage()); err != nil {
		t.Fatalf("DeliverForward with no targets: %v", err)
	}
}

func TestDeliverForward_UsesTargetFromAddress(t *testing.T) {
	target := validForwardTarget("crm")
	target.From = "specific-from@example.com"

	q := openDeliveryQueue(t, []string{"forward.crm"})
	cfg := config.Config{
		Forward: config.ForwardConfig{Targets: []config.ForwardTargetConfig{target}},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverForward(msg); err != nil {
		t.Fatalf("DeliverForward: %v", err)
	}

	entry, err := q.Next("forward.crm")
	if err != nil || entry == nil {
		t.Fatalf("expected entry in forward.crm bucket, err=%v entry=%v", err, entry)
	}
	if entry.EnvelopeFrom != target.From {
		t.Errorf("EnvelopeFrom = %q, want %q (target from address)", entry.EnvelopeFrom, target.From)
	}
}

func TestDeliverForward_PreservesOriginalRecipients(t *testing.T) {
	q := openDeliveryQueue(t, []string{"forward.crm"})
	cfg := config.Config{
		Forward: config.ForwardConfig{
			Targets: []config.ForwardTargetConfig{validForwardTarget("crm")},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	msg.EnvelopeTo = []string{"alice@example.com", "bob@example.com"}
	if err := dlv.DeliverForward(msg); err != nil {
		t.Fatalf("DeliverForward: %v", err)
	}

	entry, err := q.Next("forward.crm")
	if err != nil || entry == nil {
		t.Fatalf("expected entry in forward.crm bucket")
	}
	if len(entry.EnvelopeTo) != 2 {
		t.Fatalf("EnvelopeTo has %d recipients, want 2", len(entry.EnvelopeTo))
	}
	if entry.EnvelopeTo[0] != "alice@example.com" || entry.EnvelopeTo[1] != "bob@example.com" {
		t.Errorf("EnvelopeTo = %v, want [alice@example.com bob@example.com]", entry.EnvelopeTo)
	}
}

func TestDeliverForward_MessageBodyVerbatim(t *testing.T) {
	q := openDeliveryQueue(t, []string{"forward.crm"})
	cfg := config.Config{
		Forward: config.ForwardConfig{
			Targets: []config.ForwardTargetConfig{validForwardTarget("crm")},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	original := append([]byte(nil), msg.RawMessage...) // copy
	if err := dlv.DeliverForward(msg); err != nil {
		t.Fatalf("DeliverForward: %v", err)
	}

	entry, err := q.Next("forward.crm")
	if err != nil || entry == nil {
		t.Fatalf("expected entry in forward.crm bucket")
	}
	if string(entry.Raw) != string(original) {
		t.Errorf("forward entry body is not verbatim original\ngot:  %q\nwant: %q",
			entry.Raw, original)
	}
}

// --- Mixed archive and forward ---

func TestDeliverMixed_ArchiveAndForwardIndependent(t *testing.T) {
	buckets := []string{"archive.primary", "forward.crm"}
	q := openDeliveryQueue(t, buckets)

	cfg := config.Config{
		Archive: config.ArchiveConfig{
			Targets: []config.ArchiveTargetConfig{validArchiveTarget("primary")},
		},
		Forward: config.ForwardConfig{
			Targets: []config.ForwardTargetConfig{validForwardTarget("crm")},
		},
	}
	dlv := New(cfg, q)

	msg := testMessage()
	if err := dlv.DeliverJournal(msg); err != nil {
		t.Fatalf("DeliverJournal: %v", err)
	}
	if err := dlv.DeliverForward(msg); err != nil {
		t.Fatalf("DeliverForward: %v", err)
	}

	if !hasEntry(t, q, "archive.primary") {
		t.Error("archive.primary has no entry")
	}
	if !hasEntry(t, q, "forward.crm") {
		t.Error("forward.crm has no entry")
	}
}
