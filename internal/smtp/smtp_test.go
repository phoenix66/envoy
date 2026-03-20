package smtp

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	sasl "github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"
	"golang.org/x/crypto/bcrypt"

	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/message"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// ---- test helpers ----

// captured collects InboundMessages delivered to a handler under a mutex.
type captured struct {
	mu   sync.Mutex
	msgs []*message.InboundMessage
}

func (c *captured) handler(msg *message.InboundMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
	return nil
}

func (c *captured) first() *message.InboundMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.msgs) == 0 {
		return nil
	}
	return c.msgs[0]
}

func (c *captured) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.msgs)
}

// minimalConfig returns a config.Config with the minimum valid fields for
// starting a test server (no TLS, single domain).
func minimalConfig() config.Config {
	return config.Config{
		Server: config.ServerConfig{
			Hostname:       "localhost",
			InboundPort:    0,
			SubmissionPort: 0,
			TLS:            config.TLSConfig{Enabled: false},
		},
		Domains: []config.DomainConfig{
			{Name: "example.com", NextHop: "mx.example.com:25"},
		},
	}
}

// mustHashPassword generates a bcrypt hash of password using min cost.
func mustHashPassword(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return string(h)
}

// startTestServer creates a Server and starts it on random loopback ports.
// Returns the server, inbound listener address, and submission listener address.
// Cleanup is registered via t.Cleanup.
func startTestServer(t *testing.T, cfg config.Config, cap *captured) (inboundAddr, submissionAddr string) {
	t.Helper()

	srv, err := New(cfg, cap.handler)
	if err != nil {
		t.Fatalf("smtp.New: %v", err)
	}

	inL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen inbound: %v", err)
	}
	subL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen submission: %v", err)
	}

	go srv.Serve(inL, subL) //nolint:errcheck

	t.Cleanup(func() { srv.Close() })

	return inL.Addr().String(), subL.Addr().String()
}

// waitForMessage polls until at least one message is captured or the timeout expires.
func waitForMessage(t *testing.T, cap *captured, timeout time.Duration) *message.InboundMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if msg := cap.first(); msg != nil {
			return msg
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no message received within %v", timeout)
	return nil
}

// dialAndSend connects to addr and delivers a plain message without auth.
func dialAndSend(t *testing.T, addr, from string, to []string, body []byte) error {
	t.Helper()
	c, err := gosmtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SendMail(from, to, bytesReader(body)); err != nil {
		return err
	}
	return c.Quit()
}

// dialAuthAndSend connects, authenticates with PLAIN, then delivers a message.
func dialAuthAndSend(t *testing.T, addr, user, pass, from string, to []string, body []byte) error {
	t.Helper()
	c, err := gosmtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Auth(sasl.NewPlainClient("", user, pass)); err != nil {
		return fmt.Errorf("AUTH: %w", err)
	}
	if err := c.SendMail(from, to, bytesReader(body)); err != nil {
		return err
	}
	return c.Quit()
}

// ---- tests ----

func TestServer_InboundReceivesMessage(t *testing.T) {
	cap := &captured{}
	inAddr, _ := startTestServer(t, minimalConfig(), cap)

	body := []byte("From: sender@example.com\r\nTo: user@example.com\r\n\r\nHello\r\n")
	if err := dialAndSend(t, inAddr, "sender@example.com", []string{"user@example.com"}, body); err != nil {
		t.Fatalf("delivery: %v", err)
	}

	msg := waitForMessage(t, cap, 2*time.Second)

	if msg.EnvelopeFrom != "sender@example.com" {
		t.Errorf("EnvelopeFrom = %q, want sender@example.com", msg.EnvelopeFrom)
	}
	if len(msg.EnvelopeTo) != 1 || msg.EnvelopeTo[0] != "user@example.com" {
		t.Errorf("EnvelopeTo = %v, want [user@example.com]", msg.EnvelopeTo)
	}
	if len(msg.RawMessage) == 0 {
		t.Error("RawMessage is empty")
	}
	if msg.ID == "" {
		t.Error("ID is empty")
	}
}

func TestServer_DirectionInbound(t *testing.T) {
	cap := &captured{}
	inAddr, _ := startTestServer(t, minimalConfig(), cap)

	_ = dialAndSend(t, inAddr, "s@example.com", []string{"r@example.com"},
		[]byte("From: s@example.com\r\n\r\nbody\r\n"))

	msg := waitForMessage(t, cap, 2*time.Second)
	if msg.Direction != message.DirectionInbound {
		t.Errorf("Direction = %q, want %q", msg.Direction, message.DirectionInbound)
	}
}

func TestServer_DirectionOutbound_SubmissionPort(t *testing.T) {
	cfg := minimalConfig()
	cfg.Auth = config.AuthConfig{
		Users: []config.AuthUser{
			{Username: "user", PasswordHash: mustHashPassword(t, "secret")},
		},
	}
	cap := &captured{}
	_, subAddr := startTestServer(t, cfg, cap)

	if err := dialAuthAndSend(t, subAddr, "user", "secret",
		"user@example.com", []string{"dest@other.com"},
		[]byte("From: user@example.com\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("submission delivery: %v", err)
	}

	msg := waitForMessage(t, cap, 2*time.Second)
	if msg.Direction != message.DirectionOutbound {
		t.Errorf("Direction = %q, want %q", msg.Direction, message.DirectionOutbound)
	}
}

func TestServer_RejectsUnknownDomain(t *testing.T) {
	cap := &captured{}
	inAddr, _ := startTestServer(t, minimalConfig(), cap)

	err := dialAndSend(t, inAddr, "s@evil.com", []string{"r@notconfigured.com"},
		[]byte("From: s@evil.com\r\n\r\nbody\r\n"))
	if err == nil {
		t.Fatal("expected rejection for unknown domain, got nil")
	}
	if cap.count() != 0 {
		t.Errorf("handler called %d times, want 0 for rejected domain", cap.count())
	}
}

func TestServer_AcceptsKnownDomain(t *testing.T) {
	cfg := minimalConfig()
	cfg.Domains = append(cfg.Domains, config.DomainConfig{
		Name: "other.example.com", NextHop: "mx2.example.com:25",
	})
	cap := &captured{}
	inAddr, _ := startTestServer(t, cfg, cap)

	_ = dialAndSend(t, inAddr, "s@example.com", []string{"r@other.example.com"},
		[]byte("From: s@example.com\r\n\r\nbody\r\n"))

	waitForMessage(t, cap, 2*time.Second)
}

func TestServer_DomainFieldSetFromFirstRcpt(t *testing.T) {
	cap := &captured{}
	inAddr, _ := startTestServer(t, minimalConfig(), cap)

	_ = dialAndSend(t, inAddr, "s@example.com", []string{"user@example.com"},
		[]byte("From: s@example.com\r\n\r\nbody\r\n"))

	msg := waitForMessage(t, cap, 2*time.Second)
	if msg.Domain != "example.com" {
		t.Errorf("Domain = %q, want example.com", msg.Domain)
	}
}

func TestServer_AuthAccepted_PLAIN(t *testing.T) {
	cfg := minimalConfig()
	cfg.Auth = config.AuthConfig{
		Users: []config.AuthUser{
			{Username: "alice", PasswordHash: mustHashPassword(t, "pass123")},
		},
	}
	cap := &captured{}
	_, subAddr := startTestServer(t, cfg, cap)

	if err := dialAuthAndSend(t, subAddr, "alice", "pass123",
		"alice@example.com", []string{"bob@example.com"},
		[]byte("From: alice@example.com\r\n\r\nhello\r\n")); err != nil {
		t.Fatalf("authenticated delivery failed: %v", err)
	}
	if cap.count() == 0 {
		t.Error("handler not called after successful authentication")
	}
}

func TestServer_AuthRejected_WrongPassword(t *testing.T) {
	cfg := minimalConfig()
	cfg.Auth = config.AuthConfig{
		Users: []config.AuthUser{
			{Username: "alice", PasswordHash: mustHashPassword(t, "correct")},
		},
	}
	cap := &captured{}
	_, subAddr := startTestServer(t, cfg, cap)

	err := dialAuthAndSend(t, subAddr, "alice", "wrong",
		"alice@example.com", []string{"bob@example.com"},
		[]byte("From: alice@example.com\r\n\r\nhello\r\n"))
	if err == nil {
		t.Fatal("expected AUTH rejection, got nil")
	}
	if cap.count() != 0 {
		t.Errorf("handler called after rejected auth")
	}
}

func TestServer_AuthRejected_UnknownUser(t *testing.T) {
	cfg := minimalConfig()
	// No users configured.
	cap := &captured{}
	_, subAddr := startTestServer(t, cfg, cap)

	err := dialAuthAndSend(t, subAddr, "nobody", "anything",
		"nobody@example.com", []string{"x@example.com"},
		[]byte("From: n@e.com\r\n\r\nbody\r\n"))
	if err == nil {
		t.Fatal("expected AUTH rejection for unknown user, got nil")
	}
}

func TestServer_SubmissionRequiresAuth(t *testing.T) {
	cfg := minimalConfig()
	cfg.Auth = config.AuthConfig{
		Users: []config.AuthUser{
			{Username: "user", PasswordHash: mustHashPassword(t, "pass")},
		},
	}
	cap := &captured{}
	_, subAddr := startTestServer(t, cfg, cap)

	// Attempt to send without authenticating first.
	err := dialAndSend(t, subAddr, "user@example.com", []string{"r@example.com"},
		[]byte("From: u@e.com\r\n\r\nbody\r\n"))
	if err == nil {
		t.Fatal("expected rejection without auth on submission port, got nil")
	}
	if cap.count() != 0 {
		t.Errorf("handler called without authentication")
	}
}

func TestServer_AuthLogin_Accepted(t *testing.T) {
	cfg := minimalConfig()
	cfg.Auth = config.AuthConfig{
		Users: []config.AuthUser{
			{Username: "bob", PasswordHash: mustHashPassword(t, "letmein")},
		},
	}
	cap := &captured{}
	_, subAddr := startTestServer(t, cfg, cap)

	c, err := gosmtp.Dial(subAddr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if err := c.Auth(sasl.NewLoginClient("bob", "letmein")); err != nil {
		t.Fatalf("LOGIN AUTH failed: %v", err)
	}

	body := []byte("From: bob@example.com\r\n\r\nhello\r\n")
	if err := c.SendMail("bob@example.com", []string{"r@example.com"}, bytesReader(body)); err != nil {
		t.Fatalf("SendMail: %v", err)
	}
	_ = c.Quit()

	waitForMessage(t, cap, 2*time.Second)
}

func TestServer_MultipleRecipientsAllKnown(t *testing.T) {
	cfg := minimalConfig()
	cfg.Domains = append(cfg.Domains, config.DomainConfig{Name: "b.example.com", NextHop: "mx.b.example.com:25"})
	cap := &captured{}
	inAddr, _ := startTestServer(t, cfg, cap)

	to := []string{"a@example.com", "b@b.example.com"}
	_ = dialAndSend(t, inAddr, "s@example.com", to,
		[]byte("From: s@example.com\r\n\r\nbody\r\n"))

	msg := waitForMessage(t, cap, 2*time.Second)
	if len(msg.EnvelopeTo) != 2 {
		t.Errorf("EnvelopeTo len = %d, want 2", len(msg.EnvelopeTo))
	}
}
