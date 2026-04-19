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
			Targets: []ArchiveTargetConfig{
				{
					Name:        "primary",
					SMTPHost:    "archive.example.com",
					SMTPPort:    7700,
					JournalFrom: "journal@example.com",
					JournalTo:   "archive@example.com",
					TLS:         TLSConfig{Enabled: true},
				},
			},
		},
		Forward: ForwardConfig{
			Targets: []ForwardTargetConfig{
				{
					Name:     "crm",
					SMTPHost: "crm.example.com",
					SMTPPort: 25,
					From:     "relay@example.com",
					TLS:      TLSConfig{Enabled: true},
				},
			},
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

func TestValidate_NoArchiveTargets_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with no archive targets, got: %v", err)
	}
}

func TestValidate_NoForwardTargets_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with no forward targets, got: %v", err)
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

// --- archive targets ---

func TestValidate_ArchiveTargetMissingName(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].Name = ""
	assertError(t, cfg, "archive.targets[0].name is required")
}

func TestValidate_ArchiveTargetMissingSMTPHost(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].SMTPHost = ""
	assertError(t, cfg, "archive.targets[0].smtp_host is required")
}

func TestValidate_ArchiveTargetSMTPPortZero(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].SMTPPort = 0
	assertError(t, cfg, "archive.targets[0].smtp_port")
}

func TestValidate_ArchiveTargetSMTPPortTooHigh(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].SMTPPort = 99999
	assertError(t, cfg, "archive.targets[0].smtp_port")
}

func TestValidate_ArchiveTargetMissingJournalFrom(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].JournalFrom = ""
	assertError(t, cfg, "archive.targets[0].journal_from is required")
}

func TestValidate_ArchiveTargetMissingJournalTo(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].JournalTo = ""
	assertError(t, cfg, "archive.targets[0].journal_to is required")
}

func TestValidate_MultipleArchiveTargets_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets = append(cfg.Archive.Targets, ArchiveTargetConfig{
		Name:        "secondary",
		SMTPHost:    "archive2.example.com",
		SMTPPort:    7700,
		JournalFrom: "journal@example.com",
		JournalTo:   "archive2@example.com",
		TLS:         TLSConfig{Enabled: true},
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with two archive targets, got: %v", err)
	}
}

// --- forward targets ---

func TestValidate_ForwardTargetMissingName(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets[0].Name = ""
	assertError(t, cfg, "forward.targets[0].name is required")
}

func TestValidate_ForwardTargetMissingSMTPHost(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets[0].SMTPHost = ""
	assertError(t, cfg, "forward.targets[0].smtp_host is required")
}

func TestValidate_ForwardTargetSMTPPortZero(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets[0].SMTPPort = 0
	assertError(t, cfg, "forward.targets[0].smtp_port")
}

func TestValidate_ForwardTargetSMTPPortTooHigh(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets[0].SMTPPort = 99999
	assertError(t, cfg, "forward.targets[0].smtp_port")
}

func TestValidate_ForwardTargetMissingFrom(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets[0].From = ""
	assertError(t, cfg, "forward.targets[0].from is required")
}

func TestValidate_MultipleForwardTargets_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets = append(cfg.Forward.Targets, ForwardTargetConfig{
		Name:     "erp",
		SMTPHost: "erp.example.com",
		SMTPPort: 25,
		From:     "relay@example.com",
		TLS:      TLSConfig{Enabled: true},
	})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with two forward targets, got: %v", err)
	}
}

// --- target name uniqueness ---

func TestValidate_DuplicateArchiveTargetNames(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets = []ArchiveTargetConfig{
		{Name: "primary", SMTPHost: "a.example.com", SMTPPort: 7700,
			JournalFrom: "j@e.com", JournalTo: "a@e.com"},
		{Name: "primary", SMTPHost: "b.example.com", SMTPPort: 7700,
			JournalFrom: "j@e.com", JournalTo: "b@e.com"},
	}
	assertError(t, cfg, `"primary" duplicates`)
}

func TestValidate_DuplicateForwardTargetNames(t *testing.T) {
	cfg := validConfig()
	cfg.Forward.Targets = []ForwardTargetConfig{
		{Name: "crm", SMTPHost: "crm1.example.com", SMTPPort: 25, From: "relay@example.com"},
		{Name: "crm", SMTPHost: "crm2.example.com", SMTPPort: 25, From: "relay@example.com"},
	}
	assertError(t, cfg, `"crm" duplicates`)
}

func TestValidate_DuplicateNameAcrossLists(t *testing.T) {
	cfg := validConfig()
	// archive target and forward target share the same name
	cfg.Archive.Targets[0].Name = "shared"
	cfg.Forward.Targets[0].Name = "shared"
	assertError(t, cfg, `"shared" duplicates`)
}

func TestValidate_UniqueNamesAcrossLists_Valid(t *testing.T) {
	cfg := validConfig()
	cfg.Archive.Targets[0].Name = "archive-one"
	cfg.Forward.Targets[0].Name = "forward-one"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error with unique names across lists, got: %v", err)
	}
}

// --- applyTargetDefaults ---

func TestApplyTargetDefaults_ArchiveSMTPPort(t *testing.T) {
	cfg := &Config{
		Archive: ArchiveConfig{
			Targets: []ArchiveTargetConfig{{SMTPPort: 0}},
		},
	}
	applyTargetDefaults(cfg)
	if got := cfg.Archive.Targets[0].SMTPPort; got != 7700 {
		t.Errorf("archive SMTPPort default = %d, want 7700", got)
	}
}

func TestApplyTargetDefaults_ForwardSMTPPort(t *testing.T) {
	cfg := &Config{
		Forward: ForwardConfig{
			Targets: []ForwardTargetConfig{{SMTPPort: 0}},
		},
	}
	applyTargetDefaults(cfg)
	if got := cfg.Forward.Targets[0].SMTPPort; got != 25 {
		t.Errorf("forward SMTPPort default = %d, want 25", got)
	}
}

func TestApplyTargetDefaults_DoesNotOverrideExplicitPort(t *testing.T) {
	cfg := &Config{
		Archive: ArchiveConfig{
			Targets: []ArchiveTargetConfig{{SMTPPort: 9999}},
		},
	}
	applyTargetDefaults(cfg)
	if got := cfg.Archive.Targets[0].SMTPPort; got != 9999 {
		t.Errorf("archive SMTPPort = %d, want 9999 (explicit value should not be overridden)", got)
	}
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
	} {
		if !strings.Contains(err.Error(), substr) {
			t.Errorf("expected error to contain %q\nfull error: %v", substr, err)
		}
	}
}
