package config

import (
	"strings"
	"testing"
	"time"
)

// validConfig returns a Config that passes Validate() with no errors.
func validConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Hostname:       "mail.example.com",
			InboundPort:    25,
			SubmissionPort: 587,
			TLS: TLSConfig{
				Enabled: true,
				Cert:    "/etc/envoy/tls.crt",
				Key:     "/etc/envoy/tls.key",
			},
		},
		Domains: []DomainConfig{
			{Name: "example.com", NextHop: "mx.example.com:25"},
		},
		Archive: ArchiveConfig{
			SMTPHost:    "archive.example.com",
			SMTPPort:    7700,
			JournalFrom: "journal@example.com",
			JournalTo:   "archive@example.com",
			TLS:         TLSConfig{Enabled: true},
		},
		Queue: QueueConfig{
			Path:           "/var/spool/envoy",
			MaxRetries:     10,
			RetryIntervals: []time.Duration{time.Minute, 5 * time.Minute},
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_TLSDisabled_NoCertRequired(t *testing.T) {
	cfg := validConfig()
	cfg.Server.TLS = TLSConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with TLS disabled, got: %v", err)
	}
}

func assertError(t *testing.T, cfg *Config, substr string) {
	t.Helper()
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("expected error containing %q, got: %v", substr, err)
	}
}

// --- server ---

func TestValidate_MissingHostname(t *testing.T) {
	cfg := validConfig()
	cfg.Server.Hostname = ""
	assertError(t, cfg, "server.hostname is required")
}

func TestValidate_InboundPortZero(t *testing.T) {
	cfg := validConfig()
	cfg.Server.InboundPort = 0
	assertError(t, cfg, "server.inbound_port")
}

func TestValidate_InboundPortTooHigh(t *testing.T) {
	cfg := validConfig()
	cfg.Server.InboundPort = 65536
	assertError(t, cfg, "server.inbound_port")
}

func TestValidate_SubmissionPortZero(t *testing.T) {
	cfg := validConfig()
	cfg.Server.SubmissionPort = 0
	assertError(t, cfg, "server.submission_port")
}

func TestValidate_SubmissionPortTooHigh(t *testing.T) {
	cfg := validConfig()
	cfg.Server.SubmissionPort = 70000
	assertError(t, cfg, "server.submission_port")
}

func TestValidate_TLSEnabled_MissingCert(t *testing.T) {
	cfg := validConfig()
	cfg.Server.TLS = TLSConfig{Enabled: true, Key: "/etc/envoy/tls.key"}
	assertError(t, cfg, "server.tls.cert is required")
}

func TestValidate_TLSEnabled_MissingKey(t *testing.T) {
	cfg := validConfig()
	cfg.Server.TLS = TLSConfig{Enabled: true, Cert: "/etc/envoy/tls.crt"}
	assertError(t, cfg, "server.tls.key is required")
}

// --- domains ---

func TestValidate_NoDomains(t *testing.T) {
	cfg := validConfig()
	cfg.Domains = nil
	assertError(t, cfg, "at least one domain")
}

func TestValidate_DomainMissingName(t *testing.T) {
	cfg := validConfig()
	cfg.Domains = []DomainConfig{{Name: "", NextHop: "mx.example.com:25"}}
	assertError(t, cfg, "domains[0].name is required")
}

func TestValidate_DomainMissingNextHop(t *testing.T) {
	cfg := validConfig()
	cfg.Domains = []DomainConfig{{Name: "example.com", NextHop: ""}}
	assertError(t, cfg, "domains[0].next_hop is required")
}

func TestValidate_DomainInvalidNextHop(t *testing.T) {
	cfg := validConfig()
	cfg.Domains = []DomainConfig{{Name: "example.com", NextHop: "notahostport"}}
	assertError(t, cfg, "not a valid host:port")
}

// --- archive ---

func TestValidate_MissingArchiveSMTPHost(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.SMTPHost = ""
	assertError(t, cfg, "archive.smtp_host is required")
}

func TestValidate_ArchiveSMTPPortZero(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.SMTPPort = 0
	assertError(t, cfg, "archive.smtp_port")
}

func TestValidate_ArchiveSMTPPortTooHigh(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.SMTPPort = 99999
	assertError(t, cfg, "archive.smtp_port")
}

func TestValidate_MissingJournalFrom(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.JournalFrom = ""
	assertError(t, cfg, "archive.journal_from is required")
}

func TestValidate_MissingJournalTo(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.JournalTo = ""
	assertError(t, cfg, "archive.journal_to is required")
}

// --- queue ---

func TestValidate_NegativeMaxRetries(t *testing.T) {
	cfg := validConfig()
	cfg.Queue.MaxRetries = -1
	assertError(t, cfg, "queue.max_retries")
}

func TestValidate_ZeroRetryInterval(t *testing.T) {
	cfg := validConfig()
	cfg.Queue.RetryIntervals = []time.Duration{0}
	assertError(t, cfg, "queue.retry_intervals[0]")
}

func TestValidate_NegativeRetryInterval(t *testing.T) {
	cfg := validConfig()
	cfg.Queue.RetryIntervals = []time.Duration{time.Minute, -1 * time.Second}
	assertError(t, cfg, "queue.retry_intervals[1]")
}

// --- multiple errors ---

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected multiple errors, got nil")
	}
	for _, substr := range []string{
		"server.hostname",
		"at least one domain",
		"archive.smtp_host",
		"archive.journal_from",
		"archive.journal_to",
	} {
		if !strings.Contains(err.Error(), substr) {
			t.Errorf("expected error to contain %q\nfull error: %v", substr, err)
		}
	}
}
