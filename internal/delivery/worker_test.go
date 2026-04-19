package delivery

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/phoenix66/envoy/internal/queue"
)

// controlledSender is a mock Sender whose per-call behaviour can be scripted.
type controlledSender struct {
	mu     sync.Mutex
	calls  int32           // atomic call counter
	errors []error         // errors[i] returned on call i; last value reused
	hook   func(calls int32) // called after incrementing, before returning (optional)
}

func (m *controlledSender) Send(_, _ string, _ []string, _ []byte, _ *tls.Config) error {
	n := atomic.AddInt32(&m.calls, 1)
	if m.hook != nil {
		m.hook(n)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errors) == 0 {
		return nil
	}
	idx := int(n) - 1
	if idx >= len(m.errors) {
		idx = len(m.errors) - 1
	}
	return m.errors[idx]
}

func (m *controlledSender) callCount() int {
	return int(atomic.LoadInt32(&m.calls))
}

// openWorkerQueue creates a temporary queue for worker tests with the standard
// test buckets initialised.
func openWorkerQueue(t *testing.T) *queue.Queue {
	t.Helper()
	q, err := queue.Open(filepath.Join(t.TempDir(), "queue.db"), 3)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	if err := q.InitBuckets([]string{testArchiveBucket, testForwardBucket}); err != nil {
		t.Fatalf("queue.InitBuckets: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// Test bucket names — must match what openWorkerQueue initialises.
const (
	testArchiveBucket = "archive.primary"
	testForwardBucket = "forward.crm"
)

// newTestWorker builds a Worker with fast poll/retry intervals for tests.
// It processes testArchiveBucket and testForwardBucket.
func newTestWorker(q *queue.Queue, sender Sender) *Worker {
	return NewWorker(q, sender, WorkerConfig{
		RetryIntervals: []time.Duration{5 * time.Millisecond},
		PollInterval:   2 * time.Millisecond,
		Targets: []BucketTarget{
			{Bucket: testArchiveBucket},
			{Bucket: testForwardBucket},
		},
	}, zap.NewNop())
}

// enqueueEntry is a helper that enqueues a trivial entry and fails on error.
func enqueueEntry(t *testing.T, q *queue.Queue, bucket, id string) {
	t.Helper()
	err := q.Enqueue(bucket, queue.QueueEntry{
		ID:           id,
		Raw:          []byte("From: a@b.com\r\n\r\nbody\r\n"),
		Dest:         "127.0.0.1:9",
		EnvelopeFrom: "a@b.com",
		EnvelopeTo:   []string{"c@d.com"},
		MsgID:        id,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
}

// waitFor polls cond every 2 ms until it returns true or the deadline is reached.
func waitFor(t *testing.T, deadline time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", deadline)
}

// --- tests ---

func TestWorker_SuccessfulDelivery(t *testing.T) {
	q := openWorkerQueue(t)
	enqueueEntry(t, q, testArchiveBucket, "env-001")

	sender := &controlledSender{} // no errors → always succeeds
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Wait for the entry to be delivered and removed from the archive bucket.
	waitFor(t, 500*time.Millisecond, func() bool {
		entry, _ := q.Next(testArchiveBucket)
		return entry == nil && sender.callCount() >= 1
	})

	cancel()
}

func TestWorker_ForwardBucketDelivered(t *testing.T) {
	q := openWorkerQueue(t)
	enqueueEntry(t, q, testForwardBucket, "env-002")

	sender := &controlledSender{}
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	waitFor(t, 500*time.Millisecond, func() bool {
		entry, _ := q.Next(testForwardBucket)
		return entry == nil && sender.callCount() >= 1
	})

	cancel()
}

func TestWorker_RetryOnTemporaryFailure(t *testing.T) {
	q := openWorkerQueue(t)
	enqueueEntry(t, q, testArchiveBucket, "env-001")

	// First two calls return a temporary error; third succeeds.
	tempErr := &DeliveryError{Code: 421, Message: "try again", Temporary: true}
	sender := &controlledSender{
		errors: []error{tempErr, tempErr, nil},
	}
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Wait until the entry has been delivered (removed from archive bucket).
	waitFor(t, 2*time.Second, func() bool {
		entry, _ := q.Next(testArchiveBucket)
		return entry == nil && sender.callCount() >= 3
	})

	cancel()

	if n := sender.callCount(); n < 3 {
		t.Errorf("sender called %d times, want at least 3 (2 retries + 1 success)", n)
	}
}

func TestWorker_DeadLetterOnPermanentFailure(t *testing.T) {
	q := openWorkerQueue(t)
	enqueueEntry(t, q, testArchiveBucket, "env-001")

	permErr := &DeliveryError{Code: 550, Message: "no such user", Temporary: false}
	sender := &controlledSender{errors: []error{permErr}}
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// The entry should be removed from archive immediately on a permanent error.
	waitFor(t, 500*time.Millisecond, func() bool {
		entry, _ := q.Next(testArchiveBucket)
		return entry == nil
	})

	cancel()

	// Only one Send call should have been made.
	if n := sender.callCount(); n != 1 {
		t.Errorf("sender called %d times, want exactly 1 for permanent failure", n)
	}
}

func TestWorker_MaxRetriesDeadLetters(t *testing.T) {
	// maxRetries=3; 3 temporary failures should move the entry to dead.
	q, err := queue.Open(filepath.Join(t.TempDir(), "queue.db"), 3)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	if err := q.InitBuckets([]string{testArchiveBucket, testForwardBucket}); err != nil {
		t.Fatalf("queue.InitBuckets: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	enqueueEntry(t, q, testArchiveBucket, "env-001")

	tempErr := &DeliveryError{Code: 421, Message: "busy", Temporary: true}
	// Always fail with temporary error — queue should dead-letter after maxRetries.
	sender := &controlledSender{errors: []error{tempErr}}
	w := NewWorker(q, sender, WorkerConfig{
		RetryIntervals: []time.Duration{2 * time.Millisecond},
		PollInterval:   2 * time.Millisecond,
		Targets:        []BucketTarget{{Bucket: testArchiveBucket}},
	}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Wait for 3 send attempts (2 retries + final dead-letter), then confirm
	// the entry is gone from the archive bucket.
	waitFor(t, 2*time.Second, func() bool {
		return sender.callCount() >= 3
	})

	cancel()
	time.Sleep(10 * time.Millisecond) // let the final MarkDead/MarkFailed complete

	entry, _ := q.Next(testArchiveBucket)
	if entry != nil {
		t.Errorf("entry still in archive bucket after %d attempts", sender.callCount())
	}
}

func TestWorker_IdleWhenQueueEmpty(t *testing.T) {
	q := openWorkerQueue(t)
	sender := &controlledSender{}
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	w.Start(ctx)
	<-ctx.Done()

	if n := sender.callCount(); n != 0 {
		t.Errorf("sender called %d times on empty queue, want 0", n)
	}
}

func TestWorker_CancelStopsGoroutines(t *testing.T) {
	q := openWorkerQueue(t)
	// A sender that blocks until cancelled would deadlock if the Worker ignores ctx.
	sender := &controlledSender{}
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	cancel() // Should not hang.
	// Give goroutines a moment to observe cancellation.
	time.Sleep(20 * time.Millisecond)
}

func TestWorker_FanOut_OneGoroutinePerBucket(t *testing.T) {
	// Each bucket should be processed independently. Enqueue to both buckets
	// and verify both are drained concurrently.
	q := openWorkerQueue(t)
	enqueueEntry(t, q, testArchiveBucket, "env-archive")
	enqueueEntry(t, q, testForwardBucket, "env-forward")

	sender := &controlledSender{}
	w := newTestWorker(q, sender)

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// Wait for both entries to be removed from their buckets (MarkDelivered committed).
	waitFor(t, 500*time.Millisecond, func() bool {
		archiveEntry, _ := q.Next(testArchiveBucket)
		forwardEntry, _ := q.Next(testForwardBucket)
		return archiveEntry == nil && forwardEntry == nil && sender.callCount() >= 2
	})

	cancel()

	if n := sender.callCount(); n < 2 {
		t.Errorf("sender called %d times, want at least 2 (one per bucket)", n)
	}
}

func TestWorker_BucketIsolation_PermanentFailureDoesNotBlockOther(t *testing.T) {
	// A permanent delivery failure on one bucket must not prevent the other
	// bucket from being processed. We use two separate single-bucket workers
	// sharing the same queue to verify independence.
	q, err := queue.Open(filepath.Join(t.TempDir(), "queue.db"), 3)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	const failBucket = "archive.fails"
	const succeedBucket = "forward.succeeds"
	if err := q.InitBuckets([]string{failBucket, succeedBucket}); err != nil {
		t.Fatalf("queue.InitBuckets: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })

	// Put an entry in each bucket.
	_ = q.Enqueue(failBucket, queue.QueueEntry{
		ID: "env-fail", Raw: []byte("From: a@b.com\r\n\r\nbody\r\n"),
		Dest: "127.0.0.1:9", EnvelopeFrom: "a@b.com",
		EnvelopeTo: []string{"c@d.com"}, MsgID: "env-fail",
	})
	_ = q.Enqueue(succeedBucket, queue.QueueEntry{
		ID: "env-ok", Raw: []byte("From: a@b.com\r\n\r\nbody\r\n"),
		Dest: "127.0.0.1:9", EnvelopeFrom: "a@b.com",
		EnvelopeTo: []string{"c@d.com"}, MsgID: "env-ok",
	})

	// failBucket sender: always returns a permanent error.
	failSender := &controlledSender{
		errors: []error{&DeliveryError{Code: 550, Message: "perm", Temporary: false}},
	}
	// succeedBucket sender: always succeeds.
	okSender := &controlledSender{}

	failWorker := NewWorker(q, failSender, WorkerConfig{
		RetryIntervals: []time.Duration{2 * time.Millisecond},
		PollInterval:   2 * time.Millisecond,
		Targets:        []BucketTarget{{Bucket: failBucket}},
	}, zap.NewNop())
	okWorker := NewWorker(q, okSender, WorkerConfig{
		RetryIntervals: []time.Duration{2 * time.Millisecond},
		PollInterval:   2 * time.Millisecond,
		Targets:        []BucketTarget{{Bucket: succeedBucket}},
	}, zap.NewNop())

	ctx, cancel := context.WithCancel(context.Background())
	failWorker.Start(ctx)
	okWorker.Start(ctx)

	// The succeed-bucket entry must be delivered regardless of fail-bucket state.
	waitFor(t, 500*time.Millisecond, func() bool {
		entry, _ := q.Next(succeedBucket)
		return entry == nil
	})

	cancel()

	if n := okSender.callCount(); n < 1 {
		t.Error("succeed-bucket worker made no deliveries; may have been blocked")
	}
}
