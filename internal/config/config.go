// Package config handles configuration loading and validation.
package config

import (
	"errors"
	"fmt"
	"net"
	"reflect"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// TLSConfig holds TLS settings shared by server listeners and outbound connections.
type TLSConfig struct {
	Enabled    bool   `mapstructure:"enabled"`
	Cert       string `mapstructure:"cert"`
	Key        string `mapstructure:"key"`
	SkipVerify bool   `mapstructure:"skip_verify"`
}

// ServerConfig holds settings for the inbound SMTP listener.
type ServerConfig struct {
	Hostname       string    `mapstructure:"hostname"`
	InboundPort    int       `mapstructure:"inbound_port"`
	SubmissionPort int       `mapstructure:"submission_port"`
	TLS            TLSConfig `mapstructure:"tls"`
}

// DomainConfig describes a single accepted domain and where to relay its mail.
type DomainConfig struct {
	Name    string `mapstructure:"name"`
	NextHop string `mapstructure:"next_hop"`
}

// ArchiveConfig holds settings for the journaling archive destination.
type ArchiveConfig struct {
	SMTPHost    string    `mapstructure:"smtp_host"`
	SMTPPort    int       `mapstructure:"smtp_port"`
	JournalFrom string    `mapstructure:"journal_from"`
	JournalTo   string    `mapstructure:"journal_to"`
	TLS         TLSConfig `mapstructure:"tls"`
}

// QueueConfig holds settings for the persistent store-and-forward queue.
type QueueConfig struct {
	Path           string          `mapstructure:"path"`
	MaxRetries     int             `mapstructure:"max_retries"`
	RetryIntervals []time.Duration `mapstructure:"retry_intervals"`
}

// AuthUser is a credential entry for the submission port.
type AuthUser struct {
	Username     string `mapstructure:"username"`
	PasswordHash string `mapstructure:"password_hash"` // bcrypt hash
}

// AuthConfig holds the user list for submission-port SMTP authentication.
type AuthConfig struct {
	Users []AuthUser `mapstructure:"users"`
}

// Config is the top-level configuration structure.
type Config struct {
	Server  ServerConfig   `mapstructure:"server"`
	Domains []DomainConfig `mapstructure:"domains"`
	Archive ArchiveConfig  `mapstructure:"archive"`
	Queue   QueueConfig    `mapstructure:"queue"`
	Auth    AuthConfig     `mapstructure:"auth"`
}

// Load reads configuration from the file at path (or /etc/envoy/config.yaml if
// path is empty) and returns a fully populated, validated Config.
func Load(path string) (*Config, error) {
	v := viper.New()

	setDefaults(v)

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath("/etc/envoy")
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, viper.DecodeHook(decodeDurationHook())); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.inbound_port", 25)
	v.SetDefault("server.submission_port", 587)
	v.SetDefault("server.tls.enabled", true)

	v.SetDefault("archive.smtp_port", 7700)
	v.SetDefault("archive.tls.enabled", true)
	v.SetDefault("archive.tls.skip_verify", false)

	v.SetDefault("queue.path", "/var/spool/envoy")
	v.SetDefault("queue.max_retries", 10)
	v.SetDefault("queue.retry_intervals", []string{
		"1m", "5m", "15m", "1h", "4h", "4h", "4h",
	})
}

// decodeDurationHook returns a mapstructure decode hook that converts strings
// and string slices to time.Duration / []time.Duration.
func decodeDurationHook() mapstructure.DecodeHookFunc {
	return mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		func(f, t reflect.Type, data any) (any, error) {
			if t != reflect.TypeOf([]time.Duration{}) {
				return data, nil
			}
			if f.Kind() != reflect.Slice {
				return data, nil
			}
			raw := reflect.ValueOf(data)
			out := make([]time.Duration, raw.Len())
			for i := range raw.Len() {
				s, ok := raw.Index(i).Interface().(string)
				if !ok {
					return data, nil
				}
				d, err := time.ParseDuration(s)
				if err != nil {
					return nil, fmt.Errorf("queue.retry_intervals[%d]: %w", i, err)
				}
				out[i] = d
			}
			return out, nil
		},
	)
}

// Validate checks that all required fields are present and values are in range.
func (c *Config) Validate() error {
	var errs []error

	// server
	if c.Server.Hostname == "" {
		errs = append(errs, errors.New("server.hostname is required"))
	}
	if c.Server.InboundPort < 1 || c.Server.InboundPort > 65535 {
		errs = append(errs, fmt.Errorf("server.inbound_port %d is out of range (1–65535)", c.Server.InboundPort))
	}
	if c.Server.SubmissionPort < 1 || c.Server.SubmissionPort > 65535 {
		errs = append(errs, fmt.Errorf("server.submission_port %d is out of range (1–65535)", c.Server.SubmissionPort))
	}
	if c.Server.TLS.Enabled {
		if c.Server.TLS.Cert == "" {
			errs = append(errs, errors.New("server.tls.cert is required when server.tls.enabled is true"))
		}
		if c.Server.TLS.Key == "" {
			errs = append(errs, errors.New("server.tls.key is required when server.tls.enabled is true"))
		}
	}

	// domains
	if len(c.Domains) == 0 {
		errs = append(errs, errors.New("at least one domain must be configured"))
	}
	for i, d := range c.Domains {
		if d.Name == "" {
			errs = append(errs, fmt.Errorf("domains[%d].name is required", i))
		}
		if d.NextHop == "" {
			errs = append(errs, fmt.Errorf("domains[%d].next_hop is required", i))
		} else if _, _, err := net.SplitHostPort(d.NextHop); err != nil {
			errs = append(errs, fmt.Errorf("domains[%d].next_hop %q is not a valid host:port", i, d.NextHop))
		}
	}

	// archive
	if c.Archive.SMTPHost == "" {
		errs = append(errs, errors.New("archive.smtp_host is required"))
	}
	if c.Archive.SMTPPort < 1 || c.Archive.SMTPPort > 65535 {
		errs = append(errs, fmt.Errorf("archive.smtp_port %d is out of range (1–65535)", c.Archive.SMTPPort))
	}
	if c.Archive.JournalFrom == "" {
		errs = append(errs, errors.New("archive.journal_from is required"))
	}
	if c.Archive.JournalTo == "" {
		errs = append(errs, errors.New("archive.journal_to is required"))
	}

	// queue
	if c.Queue.MaxRetries < 0 {
		errs = append(errs, fmt.Errorf("queue.max_retries %d must be >= 0", c.Queue.MaxRetries))
	}
	for i, d := range c.Queue.RetryIntervals {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("queue.retry_intervals[%d] must be a positive duration", i))
		}
	}

	return errors.Join(errs...)
}
