package delivery

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/phoenix66/envoy/internal/queue"
)

const defaultPollInterval = 5 * time.Second

// BucketTarget associates a destination bucket with the TLS config needed
// for delivery to that destination.
type BucketTarget struct {
	// Bucket is the queue bucket name, e.g. "archive.mailarchiva" or "forward.espocrm".
	Bucket string
	// TLS is the TLS configuration for outbound delivery. nil means plain SMTP.
	TLS *tls.Config
}

// WorkerConfig holds tunable parameters for the delivery Worker.
type WorkerConfig struct {
	// RetryIntervals controls the wait between successive failed attempts.
	// The last value is reused once exhausted. Temporary failures use these;
	// permanent (5xx) failures dead-letter immediately.
	RetryIntervals []time.Duration

	// PollInterval is how long the worker sleeps when no entries are due.
	// Defaults to 5 s if zero.
	PollInterval time.Duration

	// Targets is the list of destination buckets to process. One goroutine
	// is spawned per entry.
	Targets []BucketTarget
}

// Worker processes due queue entries and delivers them via Sender.
// One independent goroutine runs per configured BucketTarget.
type Worker struct {
	q      *queue.Queue
	sender Sender
	cfg    WorkerConfig
	log    *zap.Logger
	wg     sync.WaitGroup
}

// NewWorker creates a Worker. Start must be called to begin processing.
func NewWorker(q *queue.Queue, sender Sender, cfg WorkerConfig, log *zap.Logger) *Worker {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &Worker{q: q, sender: sender, cfg: cfg, log: log}
}

// Start spawns one processing goroutine per BucketTarget. It returns
// immediately; the goroutines run until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	w.wg.Add(len(w.cfg.Targets))
	for _, t := range w.cfg.Targets {
		go func(bucket string, tlsCfg *tls.Config) {
			defer w.wg.Done()
			w.runBucket(ctx, bucket, tlsCfg)
		}(t.Bucket, t.TLS)
	}
}

// Wait blocks until all delivery goroutines have exited. Call after
// cancelling the context passed to Start.
func (w *Worker) Wait() {
	w.wg.Wait()
}

// runBucket is the per-bucket processing loop.
func (w *Worker) runBucket(ctx context.Context, bucket string, tlsCfg *tls.Config) {
	log := w.log.With(zap.String("bucket", bucket))
	log.Info("delivery worker started")
	defer log.Info("delivery worker stopped")

	for {
		if ctx.Err() != nil {
			return
		}

		entry, err := w.q.Next(bucket)
		if err != nil {
			log.Error("polling queue", zap.Error(err))
			w.sleep(ctx)
			continue
		}

		if entry == nil {
			w.sleep(ctx)
			continue
		}

		w.attempt(bucket, entry, tlsCfg)
	}
}

// attempt tries to deliver a single queue entry and records the outcome.
func (w *Worker) attempt(bucket string, entry *queue.QueueEntry, tlsCfg *tls.Config) {
	log := w.log.With(
		zap.String("bucket", bucket),
		zap.String("id", entry.ID),
		zap.String("dest", entry.Dest),
		zap.String("msg_id", entry.MsgID),
		zap.Int("attempt", entry.Attempts+1),
	)

	log.Info("attempting delivery")

	err := w.sender.Send(entry.Dest, entry.EnvelopeFrom, entry.EnvelopeTo, entry.Raw, tlsCfg)
	if err == nil {
		log.Info("delivery succeeded")
		if merr := w.q.MarkDelivered(bucket, entry.ID); merr != nil {
			log.Error("marking delivered", zap.Error(merr))
		}
		return
	}

	// Classify the error and decide whether to retry or dead-letter.
	de, ok := err.(*DeliveryError)
	if !ok {
		// Shouldn't happen — SMTPSender always returns *DeliveryError — but
		// treat unknown errors as temporary to avoid silent message loss.
		de = &DeliveryError{Message: err.Error(), Temporary: true}
	}

	if !de.Temporary {
		log.Warn("permanent delivery failure, dead-lettering",
			zap.Int("code", de.Code),
			zap.String("smtp_message", de.Message),
		)
		if merr := w.q.MarkDead(bucket, entry.ID); merr != nil {
			log.Error("marking dead", zap.Error(merr))
		}
		return
	}

	log.Warn("temporary delivery failure, scheduling retry",
		zap.Int("code", de.Code),
		zap.String("smtp_message", de.Message),
	)
	if merr := w.q.MarkFailed(bucket, entry.ID, w.cfg.RetryIntervals); merr != nil {
		log.Error("marking failed", zap.Error(merr))
	}
}

// sleep pauses until PollInterval elapses or ctx is cancelled.
func (w *Worker) sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(w.cfg.PollInterval):
	}
}
