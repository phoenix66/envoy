// Package queue implements a persistent store-and-forward queue backed by bbolt.
package queue

import (
	"encoding/json"
	"fmt"
	"time"

	bbolt "go.etcd.io/bbolt"
)

// Named buckets for active work queues.
const (
	BucketForward = "forward"
	BucketJournal = "journal"
)

// Internal terminal buckets — not exposed for enqueue.
const (
	bucketDead      = "dead"
	bucketDelivered = "delivered"
)

var allBuckets = []string{BucketForward, BucketJournal, bucketDead, bucketDelivered}

// QueueEntry is a single item in the persistent queue.
type QueueEntry struct {
	// ID is the unique queue entry key, used as the bbolt key.
	// Callers should use message.NewID() to generate this.
	ID string `json:"id"`

	// Raw holds the bytes to deliver: the original message for forward
	// entries, or the journal envelope for journal entries.
	Raw []byte `json:"raw"`

	// Dest is the target host:port for delivery.
	Dest string `json:"dest"`

	// EnvelopeFrom is the RFC 5321 MAIL FROM address for the outbound connection.
	EnvelopeFrom string `json:"envelope_from"`

	// EnvelopeTo holds the RFC 5321 RCPT TO addresses for the outbound connection.
	EnvelopeTo []string `json:"envelope_to"`

	// Attempts is the number of delivery attempts made so far.
	Attempts int `json:"attempts"`

	// NextRetry is the earliest time the entry should next be attempted.
	// A zero value means the entry is immediately eligible.
	NextRetry time.Time `json:"next_retry"`

	// MsgID is the originating InboundMessage ID, kept for log correlation.
	MsgID string `json:"msg_id"`
}

// Queue is a persistent store-and-forward queue backed by a bbolt database.
type Queue struct {
	db         *bbolt.DB
	maxRetries int
}

// Open opens (or creates) the queue database at path. maxRetries controls
// how many failed attempts are allowed before an entry is dead-lettered.
func Open(path string, maxRetries int) (*Queue, error) {
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("queue: opening database: %w", err)
	}

	q := &Queue{db: db, maxRetries: maxRetries}
	if err := q.createBuckets(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return q, nil
}

// Close closes the underlying database.
func (q *Queue) Close() error {
	return q.db.Close()
}

// createBuckets ensures all required buckets exist.
func (q *Queue) createBuckets() error {
	return q.db.Update(func(tx *bbolt.Tx) error {
		for _, name := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return fmt.Errorf("queue: creating bucket %q: %w", name, err)
			}
		}
		return nil
	})
}

// Enqueue adds entry to bucket. bucket must be BucketForward or BucketJournal.
func (q *Queue) Enqueue(bucket string, entry QueueEntry) error {
	if bucket != BucketForward && bucket != BucketJournal {
		return fmt.Errorf("queue: %q is not a valid work bucket", bucket)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("queue: marshaling entry: %w", err)
	}
	return q.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte(bucket)).Put([]byte(entry.ID), data)
	})
}

// Next returns the first entry in bucket whose NextRetry is at or before now,
// or nil if no entry is currently due. The entry remains in the bucket until
// MarkDelivered or MarkFailed is called.
func (q *Queue) Next(bucket string) (*QueueEntry, error) {
	now := time.Now()
	var found *QueueEntry

	err := q.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("queue: bucket %q not found", bucket)
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var e QueueEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return fmt.Errorf("queue: unmarshaling entry %q: %w", string(k), err)
			}
			if !e.NextRetry.After(now) {
				found = &e
				return nil
			}
		}
		return nil
	})
	return found, err
}

// MarkDelivered removes id from bucket and archives it in the delivered log.
func (q *Queue) MarkDelivered(bucket, id string) error {
	return q.db.Update(func(tx *bbolt.Tx) error {
		src := tx.Bucket([]byte(bucket))
		if src == nil {
			return fmt.Errorf("queue: bucket %q not found", bucket)
		}
		data := src.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("queue: entry %q not found in bucket %q", id, bucket)
		}
		if err := src.Delete([]byte(id)); err != nil {
			return err
		}
		return tx.Bucket([]byte(bucketDelivered)).Put([]byte(id), data)
	})
}

// MarkFailed increments the attempt count for id in bucket and schedules the
// next retry. retryIntervals defines the wait time after each attempt; the
// last value is reused once all intervals are exhausted. If the attempt count
// reaches maxRetries the entry is moved to the dead-letter bucket.
func (q *Queue) MarkFailed(bucket, id string, retryIntervals []time.Duration) error {
	return q.db.Update(func(tx *bbolt.Tx) error {
		src := tx.Bucket([]byte(bucket))
		if src == nil {
			return fmt.Errorf("queue: bucket %q not found", bucket)
		}
		data := src.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("queue: entry %q not found in bucket %q", id, bucket)
		}

		var entry QueueEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return fmt.Errorf("queue: unmarshaling entry %q: %w", id, err)
		}

		entry.Attempts++

		if q.maxRetries > 0 && entry.Attempts >= q.maxRetries {
			if err := src.Delete([]byte(id)); err != nil {
				return err
			}
			dead, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("queue: marshaling dead entry: %w", err)
			}
			return tx.Bucket([]byte(bucketDead)).Put([]byte(id), dead)
		}

		if len(retryIntervals) > 0 {
			idx := entry.Attempts - 1
			if idx >= len(retryIntervals) {
				idx = len(retryIntervals) - 1
			}
			entry.NextRetry = time.Now().Add(retryIntervals[idx])
		}

		updated, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("queue: marshaling entry: %w", err)
		}
		return src.Put([]byte(id), updated)
	})
}

// MarkDead moves entry id from bucket directly to the dead-letter bucket,
// regardless of attempt count. Use this for permanent (5xx) delivery failures.
func (q *Queue) MarkDead(bucket, id string) error {
	return q.db.Update(func(tx *bbolt.Tx) error {
		src := tx.Bucket([]byte(bucket))
		if src == nil {
			return fmt.Errorf("queue: bucket %q not found", bucket)
		}
		data := src.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("queue: entry %q not found in bucket %q", id, bucket)
		}
		if err := src.Delete([]byte(id)); err != nil {
			return err
		}
		return tx.Bucket([]byte(bucketDead)).Put([]byte(id), data)
	})
}

// PurgeDelivered removes all entries from the delivered log. This is safe to
// call at any time; it does not affect active or dead-letter entries.
func (q *Queue) PurgeDelivered() error {
	return q.db.Update(func(tx *bbolt.Tx) error {
		if err := tx.DeleteBucket([]byte(bucketDelivered)); err != nil {
			return fmt.Errorf("queue: deleting delivered bucket: %w", err)
		}
		if _, err := tx.CreateBucket([]byte(bucketDelivered)); err != nil {
			return fmt.Errorf("queue: recreating delivered bucket: %w", err)
		}
		return nil
	})
}
