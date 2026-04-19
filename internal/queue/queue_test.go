package queue

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	bbolt "go.etcd.io/bbolt"
)

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// Test bucket names following the archive.<name> / forward.<name> scheme.
const (
	testArchiveBucket = "archive.test"
	testForwardBucket = "forward.test"
)

// openTestQueue creates a Queue backed by a temp bbolt file and registers
// cleanup via t.Cleanup. The test buckets are initialised via InitBuckets.
func openTestQueue(t *testing.T, maxRetries int) *Queue {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	q, err := Open(path, maxRetries)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := q.InitBuckets([]string{testArchiveBucket, testForwardBucket}); err != nil {
		t.Fatalf("InitBuckets: %v", err)
	}
	t.Cleanup(func() { _ = q.Close() })
	return q
}

// sampleEntry returns a ready-to-deliver QueueEntry with a zero NextRetry.
func sampleEntry(id string) QueueEntry {
	return QueueEntry{
		ID:        id,
		Raw:       []byte("raw message bytes"),
		Dest:      "mx.example.com:25",
		Attempts:  0,
		NextRetry: time.Time{}, // zero == immediately due
		MsgID:     id,
	}
}

// countBucket returns the number of keys in the named bucket.
func countBucket(t *testing.T, q *Queue, bucket string) int {
	t.Helper()
	var n int
	err := q.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	if err != nil {
		t.Fatalf("countBucket %q: %v", bucket, err)
	}
	return n
}

// --- InitBuckets ---

func TestInitBuckets_CreatesLiveBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	q, err := Open(path, 10)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer q.Close()

	names := []string{"archive.primary", "forward.crm"}
	if err := q.InitBuckets(names); err != nil {
		t.Fatalf("InitBuckets: %v", err)
	}

	for _, name := range names {
		if err := q.db.View(func(tx *bbolt.Tx) error {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("bucket %q not found", name)
			}
			return nil
		}); err != nil {
			t.Errorf("live bucket %q not created: %v", name, err)
		}
	}
}

func TestInitBuckets_CreatesDeadBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	q, err := Open(path, 10)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer q.Close()

	names := []string{"archive.primary", "forward.crm"}
	if err := q.InitBuckets(names); err != nil {
		t.Fatalf("InitBuckets: %v", err)
	}

	for _, name := range names {
		dead := "dead." + name
		if err := q.db.View(func(tx *bbolt.Tx) error {
			if tx.Bucket([]byte(dead)) == nil {
				return fmt.Errorf("bucket %q not found", dead)
			}
			return nil
		}); err != nil {
			t.Errorf("dead bucket %q not created: %v", dead, err)
		}
	}
}

func TestInitBuckets_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	q, err := Open(path, 10)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer q.Close()

	names := []string{"archive.primary"}
	if err := q.InitBuckets(names); err != nil {
		t.Fatalf("first InitBuckets: %v", err)
	}
	if err := q.InitBuckets(names); err != nil {
		t.Errorf("second InitBuckets (idempotent): %v", err)
	}
}

func TestInitBuckets_EmptyList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	q, err := Open(path, 10)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer q.Close()

	if err := q.InitBuckets(nil); err != nil {
		t.Errorf("InitBuckets(nil): %v", err)
	}
}

// --- Enqueue ---

func TestEnqueue_ValidBuckets(t *testing.T) {
	q := openTestQueue(t, 10)
	for _, bucket := range []string{testArchiveBucket, testForwardBucket} {
		if err := q.Enqueue(bucket, sampleEntry("id-"+bucket)); err != nil {
			t.Errorf("Enqueue(%q): %v", bucket, err)
		}
	}
}

func TestEnqueue_UnknownBucketReturnsError(t *testing.T) {
	q := openTestQueue(t, 10)
	if err := q.Enqueue("archive.nonexistent", sampleEntry("x")); err == nil {
		t.Error("expected error for unknown bucket, got nil")
	}
}

func TestEnqueue_EntryPersisted(t *testing.T) {
	q := openTestQueue(t, 10)
	entry := sampleEntry("env-001")
	if err := q.Enqueue(testArchiveBucket, entry); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := countBucket(t, q, testArchiveBucket); got != 1 {
		t.Errorf("archive bucket count = %d, want 1", got)
	}
}

// --- Next ---

func TestNext_ReturnsDueEntry(t *testing.T) {
	q := openTestQueue(t, 10)
	entry := sampleEntry("env-001")
	_ = q.Enqueue(testArchiveBucket, entry)

	got, err := q.Next(testArchiveBucket)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got == nil {
		t.Fatal("Next returned nil, want entry")
	}
	if got.ID != entry.ID {
		t.Errorf("Next ID = %q, want %q", got.ID, entry.ID)
	}
}

func TestNext_NilWhenNothingDue(t *testing.T) {
	q := openTestQueue(t, 10)
	entry := sampleEntry("env-future")
	entry.NextRetry = time.Now().Add(1 * time.Hour)
	_ = q.Enqueue(testArchiveBucket, entry)

	got, err := q.Next(testArchiveBucket)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != nil {
		t.Errorf("Next returned entry %q, want nil", got.ID)
	}
}

func TestNext_NilWhenEmpty(t *testing.T) {
	q := openTestQueue(t, 10)
	got, err := q.Next(testArchiveBucket)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got != nil {
		t.Errorf("Next returned %q on empty queue, want nil", got.ID)
	}
}

func TestNext_DoesNotRemoveEntry(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	_, _ = q.Next(testArchiveBucket)
	if got := countBucket(t, q, testArchiveBucket); got != 1 {
		t.Errorf("archive bucket count after Next = %d, want 1 (Next must not consume)", got)
	}
}

// --- MarkDelivered ---

func TestMarkDelivered_RemovesFromActive(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	if err := q.MarkDelivered(testArchiveBucket, "env-001"); err != nil {
		t.Fatalf("MarkDelivered: %v", err)
	}
	if got := countBucket(t, q, testArchiveBucket); got != 0 {
		t.Errorf("archive bucket count = %d, want 0 after delivery", got)
	}
}

func TestMarkDelivered_ArchivesInDelivered(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))
	_ = q.MarkDelivered(testArchiveBucket, "env-001")

	if got := countBucket(t, q, bucketDelivered); got != 1 {
		t.Errorf("delivered bucket count = %d, want 1", got)
	}
}

func TestMarkDelivered_NotFound(t *testing.T) {
	q := openTestQueue(t, 10)
	if err := q.MarkDelivered(testArchiveBucket, "does-not-exist"); err == nil {
		t.Error("expected error for missing entry, got nil")
	}
}

// --- MarkFailed / retry scheduling ---

func TestMarkFailed_IncrementsAttempts(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	intervals := []time.Duration{time.Minute, 5 * time.Minute}
	if err := q.MarkFailed(testArchiveBucket, "env-001", intervals); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	got, _ := q.Next(testArchiveBucket) // won't be nil only if still immediately due
	// After first failure entry has a future NextRetry, so Next returns nil.
	if got != nil {
		t.Errorf("entry should not be immediately due after MarkFailed")
	}
	// Confirm it is still in the bucket (not dead-lettered yet).
	if n := countBucket(t, q, testArchiveBucket); n != 1 {
		t.Errorf("archive bucket count = %d, want 1", n)
	}
}

func TestMarkFailed_SchedulesNextRetry(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	before := time.Now()
	interval := 5 * time.Minute
	_ = q.MarkFailed(testArchiveBucket, "env-001", []time.Duration{interval})

	// Re-read the entry directly to inspect NextRetry.
	got, err := q.Next(testArchiveBucket)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	// Entry must not be due yet.
	if got != nil {
		t.Errorf("entry should not be due after scheduling %v retry", interval)
	}

	// Verify NextRetry is roughly before+interval (within a second of scheduling).
	var entry QueueEntry
	q.db.View(func(tx *bbolt.Tx) error { //nolint:errcheck
		v := tx.Bucket([]byte(testArchiveBucket)).Get([]byte("env-001"))
		return jsonUnmarshal(v, &entry)
	})
	low := before.Add(interval - time.Second)
	high := before.Add(interval + time.Second)
	if entry.NextRetry.Before(low) || entry.NextRetry.After(high) {
		t.Errorf("NextRetry = %v, want ~%v (+/- 1s)", entry.NextRetry, before.Add(interval))
	}
}

func TestMarkFailed_UsesLastIntervalWhenExhausted(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	intervals := []time.Duration{time.Second, 2 * time.Second}

	// Fail twice to exhaust intervals, then fail a third time.
	_ = q.MarkFailed(testArchiveBucket, "env-001", intervals) // attempts=1, uses intervals[0]
	// Manually reset NextRetry so Next() can find it again.
	resetNextRetry(t, q, testArchiveBucket, "env-001")
	_ = q.MarkFailed(testArchiveBucket, "env-001", intervals) // attempts=2, uses intervals[1]
	resetNextRetry(t, q, testArchiveBucket, "env-001")

	before := time.Now()
	_ = q.MarkFailed(testArchiveBucket, "env-001", intervals) // attempts=3, clamps to intervals[1]

	var entry QueueEntry
	q.db.View(func(tx *bbolt.Tx) error { //nolint:errcheck
		v := tx.Bucket([]byte(testArchiveBucket)).Get([]byte("env-001"))
		return jsonUnmarshal(v, &entry)
	})
	low := before.Add(intervals[1] - time.Second)
	high := before.Add(intervals[1] + time.Second)
	if entry.NextRetry.Before(low) || entry.NextRetry.After(high) {
		t.Errorf("NextRetry = %v after exhausted intervals, want ~%v (+/- 1s)",
			entry.NextRetry, before.Add(intervals[1]))
	}
}

// --- Dead-letter behaviour ---

func TestMarkFailed_DeadLettersOnMaxRetries(t *testing.T) {
	const maxRetries = 3
	q := openTestQueue(t, maxRetries)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	intervals := []time.Duration{time.Millisecond}
	deadBucket := "dead." + testArchiveBucket

	for range maxRetries - 1 {
		resetNextRetry(t, q, testArchiveBucket, "env-001")
		if err := q.MarkFailed(testArchiveBucket, "env-001", intervals); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
	}
	// Confirm still in active bucket.
	if n := countBucket(t, q, testArchiveBucket); n != 1 {
		t.Fatalf("archive bucket count = %d before final failure, want 1", n)
	}

	// Final failure should dead-letter.
	resetNextRetry(t, q, testArchiveBucket, "env-001")
	if err := q.MarkFailed(testArchiveBucket, "env-001", intervals); err != nil {
		t.Fatalf("MarkFailed (final): %v", err)
	}

	if n := countBucket(t, q, testArchiveBucket); n != 0 {
		t.Errorf("archive bucket count = %d after dead-lettering, want 0", n)
	}
	if n := countBucket(t, q, deadBucket); n != 1 {
		t.Errorf("dead bucket count = %d, want 1", n)
	}
}

func TestMarkFailed_DeadEntryRetainsAttemptCount(t *testing.T) {
	const maxRetries = 2
	q := openTestQueue(t, maxRetries)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))
	deadBucket := "dead." + testArchiveBucket

	intervals := []time.Duration{time.Millisecond}
	resetNextRetry(t, q, testArchiveBucket, "env-001")
	_ = q.MarkFailed(testArchiveBucket, "env-001", intervals) // attempts=1
	resetNextRetry(t, q, testArchiveBucket, "env-001")
	_ = q.MarkFailed(testArchiveBucket, "env-001", intervals) // attempts=2 >= 2: dead

	var dead QueueEntry
	q.db.View(func(tx *bbolt.Tx) error { //nolint:errcheck
		v := tx.Bucket([]byte(deadBucket)).Get([]byte("env-001"))
		return jsonUnmarshal(v, &dead)
	})
	if dead.Attempts != maxRetries {
		t.Errorf("dead entry Attempts = %d, want %d", dead.Attempts, maxRetries)
	}
}

func TestMarkDead_MovesToDeadBucket(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testForwardBucket, sampleEntry("env-001"))
	deadBucket := "dead." + testForwardBucket

	if err := q.MarkDead(testForwardBucket, "env-001"); err != nil {
		t.Fatalf("MarkDead: %v", err)
	}
	if n := countBucket(t, q, testForwardBucket); n != 0 {
		t.Errorf("forward bucket count = %d after MarkDead, want 0", n)
	}
	if n := countBucket(t, q, deadBucket); n != 1 {
		t.Errorf("dead forward bucket count = %d after MarkDead, want 1", n)
	}
}

// --- PurgeDelivered ---

func TestPurgeDelivered_EmptiesDeliveredBucket(t *testing.T) {
	q := openTestQueue(t, 10)
	for i, id := range []string{"env-001", "env-002", "env-003"} {
		_ = q.Enqueue(testArchiveBucket, sampleEntry(id))
		_ = q.MarkDelivered(testArchiveBucket, id)
		_ = i
	}
	if n := countBucket(t, q, bucketDelivered); n != 3 {
		t.Fatalf("delivered count before purge = %d, want 3", n)
	}
	if err := q.PurgeDelivered(); err != nil {
		t.Fatalf("PurgeDelivered: %v", err)
	}
	if n := countBucket(t, q, bucketDelivered); n != 0 {
		t.Errorf("delivered count after purge = %d, want 0", n)
	}
}

func TestPurgeDelivered_DoesNotAffectOtherBuckets(t *testing.T) {
	q := openTestQueue(t, 10)
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-active"))
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-deliver"))
	_ = q.MarkDelivered(testArchiveBucket, "env-deliver")

	_ = q.PurgeDelivered()

	if n := countBucket(t, q, testArchiveBucket); n != 1 {
		t.Errorf("archive bucket count = %d after purge, want 1 (active must be unaffected)", n)
	}
}

func TestPurgeDelivered_Idempotent(t *testing.T) {
	q := openTestQueue(t, 10)
	if err := q.PurgeDelivered(); err != nil {
		t.Errorf("PurgeDelivered on empty delivered bucket: %v", err)
	}
}

// --- Multiple independent buckets ---

func TestIndependentBuckets_EntriesIsolated(t *testing.T) {
	q := openTestQueue(t, 10)

	// Enqueue to archive bucket only.
	_ = q.Enqueue(testArchiveBucket, sampleEntry("env-001"))

	// Forward bucket should be empty.
	if n := countBucket(t, q, testForwardBucket); n != 0 {
		t.Errorf("forward bucket has %d entries, want 0 (should be isolated)", n)
	}
	if n := countBucket(t, q, testArchiveBucket); n != 1 {
		t.Errorf("archive bucket has %d entries, want 1", n)
	}
}

// --- helpers ---

// resetNextRetry sets an entry's NextRetry to zero so Next() returns it again.
func resetNextRetry(t *testing.T, q *Queue, bucket, id string) {
	t.Helper()
	err := q.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		data := b.Get([]byte(id))
		if data == nil {
			return nil
		}
		var e QueueEntry
		if err := jsonUnmarshal(data, &e); err != nil {
			return err
		}
		e.NextRetry = time.Time{}
		updated, err := jsonMarshal(e)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), updated)
	})
	if err != nil {
		t.Fatalf("resetNextRetry %q: %v", id, err)
	}
}
