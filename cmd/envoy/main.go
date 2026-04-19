package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/delivery"
	"github.com/phoenix66/envoy/internal/message"
	"github.com/phoenix66/envoy/internal/queue"
	smtpsrv "github.com/phoenix66/envoy/internal/smtp"
)

const shutdownTimeout = 30 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "envoy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ---- flags ----
	configPath := flag.String("config", "", "path to config file (default: /etc/envoy/config.yaml)")
	devMode := flag.Bool("dev", false, "use development logger (console output)")
	flag.Parse()

	// ---- logger ----
	log, err := buildLogger(*devMode)
	if err != nil {
		return fmt.Errorf("initialising logger: %w", err)
	}
	defer log.Sync() //nolint:errcheck

	// ---- config ----
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	log.Info("configuration loaded", zap.String("hostname", cfg.Server.Hostname))

	// ---- queue ----
	if err := os.MkdirAll(cfg.Queue.Path, 0o750); err != nil {
		return fmt.Errorf("creating queue directory: %w", err)
	}
	dbPath := filepath.Join(cfg.Queue.Path, "queue.db")
	q, err := queue.Open(dbPath, cfg.Queue.MaxRetries)
	if err != nil {
		return fmt.Errorf("opening queue: %w", err)
	}
	log.Info("queue opened", zap.String("path", dbPath))

	// ---- initialise per-destination buckets ----
	bucketNames := collectBucketNames(cfg)
	if err := q.InitBuckets(bucketNames); err != nil {
		return fmt.Errorf("initialising queue buckets: %w", err)
	}
	log.Info("queue buckets initialised", zap.Int("count", len(bucketNames)))

	// ---- deliverer ----
	dlv := delivery.New(*cfg, q)

	// ---- delivery worker targets ----
	var workerTargets []delivery.BucketTarget
	for _, t := range cfg.Archive.Targets {
		workerTargets = append(workerTargets, delivery.BucketTarget{
			Bucket: "archive." + t.Name,
			TLS:    buildTLSConfig(t.TLS, t.SMTPHost),
		})
	}
	for _, t := range cfg.Forward.Targets {
		workerTargets = append(workerTargets, delivery.BucketTarget{
			Bucket: "forward." + t.Name,
			TLS:    buildTLSConfig(t.TLS, t.SMTPHost),
		})
	}

	// ---- delivery worker ----
	worker := delivery.NewWorker(q, &delivery.SMTPSender{}, delivery.WorkerConfig{
		RetryIntervals: cfg.Queue.RetryIntervals,
		Targets:        workerTargets,
	}, log.Named("worker"))

	// ---- message handler ----
	handler := func(msg *message.InboundMessage) error {
		log.Info("message received",
			zap.String("id", msg.ID),
			zap.String("direction", string(msg.Direction)),
			zap.String("from", msg.EnvelopeFrom),
			zap.Int("recipient_count", len(msg.EnvelopeTo)),
			zap.String("domain", msg.Domain),
		)

		// Enqueue journal envelope for each configured archive target.
		if err := dlv.DeliverJournal(msg); err != nil {
			log.Error("enqueuing journal delivery",
				zap.String("id", msg.ID),
				zap.Error(err),
			)
		} else {
			log.Info("journal delivery enqueued",
				zap.String("id", msg.ID),
				zap.Int("archive_targets", len(cfg.Archive.Targets)),
			)
		}

		// Enqueue verbatim message for each configured forward target.
		if err := dlv.DeliverForward(msg); err != nil {
			log.Error("enqueuing forward delivery",
				zap.String("id", msg.ID),
				zap.Error(err),
			)
		} else if len(cfg.Forward.Targets) > 0 {
			log.Info("forward delivery enqueued",
				zap.String("id", msg.ID),
				zap.Int("forward_targets", len(cfg.Forward.Targets)),
			)
		}

		return nil
	}

	// ---- SMTP server ----
	srv, err := smtpsrv.New(*cfg, handler)
	if err != nil {
		return fmt.Errorf("initialising SMTP server: %w", err)
	}

	// ---- start workers ----
	workerCtx, workerCancel := context.WithCancel(context.Background())
	worker.Start(workerCtx)
	log.Info("delivery workers started", zap.Int("goroutines", len(workerTargets)))

	// ---- start SMTP server ----
	serverErrc := make(chan error, 1)
	go func() {
		log.Info("SMTP server starting",
			zap.Int("inbound_port", cfg.Server.InboundPort),
			zap.Int("submission_port", cfg.Server.SubmissionPort),
		)
		if err := srv.ListenAndServe(); err != nil {
			serverErrc <- err
		}
	}()

	// ---- wait for signal or server error ----
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigc:
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
	case err := <-serverErrc:
		log.Error("SMTP server stopped unexpectedly", zap.Error(err))
	}

	// ---- graceful shutdown ----
	log.Info("shutting down", zap.Duration("timeout", shutdownTimeout))

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	// 1. Stop accepting new SMTP connections.
	if err := srv.Close(); err != nil {
		log.Error("closing SMTP server", zap.Error(err))
	}
	log.Info("SMTP server closed")

	// 2. Cancel delivery workers and wait for all goroutines to finish.
	workerCancel()

	workerDone := make(chan struct{})
	go func() {
		worker.Wait()
		close(workerDone)
	}()

	select {
	case <-workerDone:
		log.Info("delivery workers stopped")
	case <-shutdownCtx.Done():
		log.Warn("shutdown timeout: some in-flight deliveries may not have completed")
	}

	// 3. Close the queue database.
	if err := q.Close(); err != nil {
		log.Error("closing queue", zap.Error(err))
	}
	log.Info("queue closed")

	log.Info("envoy stopped")
	return nil
}

// collectBucketNames builds the list of per-destination bucket names from all
// configured archive and forward targets.
func collectBucketNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Archive.Targets)+len(cfg.Forward.Targets))
	for _, t := range cfg.Archive.Targets {
		names = append(names, "archive."+t.Name)
	}
	for _, t := range cfg.Forward.Targets {
		names = append(names, "forward."+t.Name)
	}
	return names
}

// buildLogger returns a zap logger. Development mode uses a human-readable
// console encoder; production uses structured JSON.
func buildLogger(dev bool) (*zap.Logger, error) {
	if dev {
		return zap.NewDevelopment()
	}
	return zap.NewProduction()
}

// buildTLSConfig constructs a *tls.Config from TLS settings.
// Returns nil if TLS is disabled, which causes the sender to use plain SMTP.
func buildTLSConfig(cfg config.TLSConfig, serverName string) *tls.Config {
	if !cfg.Enabled {
		return nil
	}
	return &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: cfg.SkipVerify, //nolint:gosec // operator-controlled opt-in
	}
}
