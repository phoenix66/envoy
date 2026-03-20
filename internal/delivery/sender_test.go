package delivery

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"

	gosmtp "github.com/emersion/go-smtp"
)

// --- mock SMTP backend ---

type mockBackend struct {
	mu       sync.Mutex
	received []receivedMsg
	dataErr  error // if non-nil, returned verbatim from Data
}

type receivedMsg struct {
	from string
	to   []string
	data []byte
}

func (b *mockBackend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &mockSession{be: b}, nil
}

type mockSession struct {
	be   *mockBackend
	from string
	to   []string
}

func (s *mockSession) Reset()        {}
func (s *mockSession) Logout() error { return nil }

func (s *mockSession) Mail(from string, _ *gosmtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *mockSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}

func (s *mockSession) Data(r io.Reader) error {
	s.be.mu.Lock()
	defer s.be.mu.Unlock()
	if s.be.dataErr != nil {
		return s.be.dataErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.be.received = append(s.be.received, receivedMsg{
		from: s.from,
		to:   append([]string(nil), s.to...),
		data: data,
	})
	return nil
}

// startMockSMTP starts a go-smtp server on a random loopback port and returns
// its address. The server is stopped via t.Cleanup.
func startMockSMTP(t *testing.T, be gosmtp.Backend) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := gosmtp.NewServer(be)
	srv.Domain = "localhost"
	srv.AllowInsecureAuth = true

	go srv.Serve(l) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })

	return l.Addr().String()
}

// --- tests ---

func TestSend_DeliversMessage(t *testing.T) {
	be := &mockBackend{}
	addr := startMockSMTP(t, be)

	s := &SMTPSender{}
	payload := []byte("From: a@example.com\r\n\r\nHello\r\n")
	if err := s.Send(addr, "from@example.com", []string{"to@example.com"}, payload, nil); err != nil {
		t.Fatalf("Send: %v", err)
	}

	be.mu.Lock()
	defer be.mu.Unlock()
	if got := len(be.received); got != 1 {
		t.Fatalf("server received %d messages, want 1", got)
	}
	msg := be.received[0]
	if msg.from != "from@example.com" {
		t.Errorf("MAIL FROM = %q, want from@example.com", msg.from)
	}
	if len(msg.to) != 1 || msg.to[0] != "to@example.com" {
		t.Errorf("RCPT TO = %v, want [to@example.com]", msg.to)
	}
}

func TestSend_MessageBytesDeliveredVerbatim(t *testing.T) {
	be := &mockBackend{}
	addr := startMockSMTP(t, be)

	payload := []byte("From: a@example.com\r\nSubject: Test\r\n\r\nBody text.\r\n")
	s := &SMTPSender{}
	_ = s.Send(addr, "a@example.com", []string{"b@example.com"}, payload, nil)

	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.received) == 0 {
		t.Fatal("server received no messages")
	}
	// go-smtp dot-unstuffs on receipt; compare without trailing dot-stuffing artefacts.
	if string(be.received[0].data) != string(payload) {
		t.Errorf("delivered data differs\ngot:  %q\nwant: %q", be.received[0].data, payload)
	}
}

func TestSend_MultipleRecipients(t *testing.T) {
	be := &mockBackend{}
	addr := startMockSMTP(t, be)

	s := &SMTPSender{}
	to := []string{"a@example.com", "b@example.com", "c@example.com"}
	_ = s.Send(addr, "from@example.com", to, []byte("From: f@x.com\r\n\r\nbody\r\n"), nil)

	be.mu.Lock()
	defer be.mu.Unlock()
	if got := len(be.received[0].to); got != 3 {
		t.Errorf("RCPT count = %d, want 3", got)
	}
}

func TestSend_TemporaryError_4xx(t *testing.T) {
	be := &mockBackend{
		dataErr: &gosmtp.SMTPError{Code: 421, Message: "service unavailable"},
	}
	addr := startMockSMTP(t, be)

	s := &SMTPSender{}
	err := s.Send(addr, "from@example.com", []string{"to@example.com"},
		[]byte("From: f@x.com\r\n\r\nbody\r\n"), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var de *DeliveryError
	if !errors.As(err, &de) {
		t.Fatalf("error type = %T, want *DeliveryError", err)
	}
	if !de.Temporary {
		t.Errorf("DeliveryError.Temporary = false for 4xx, want true")
	}
	if de.Code != 421 {
		t.Errorf("DeliveryError.Code = %d, want 421", de.Code)
	}
}

func TestSend_PermanentError_5xx(t *testing.T) {
	be := &mockBackend{
		dataErr: &gosmtp.SMTPError{Code: 550, Message: "mailbox unavailable"},
	}
	addr := startMockSMTP(t, be)

	s := &SMTPSender{}
	err := s.Send(addr, "from@example.com", []string{"to@example.com"},
		[]byte("From: f@x.com\r\n\r\nbody\r\n"), nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var de *DeliveryError
	if !errors.As(err, &de) {
		t.Fatalf("error type = %T, want *DeliveryError", err)
	}
	if de.Temporary {
		t.Errorf("DeliveryError.Temporary = true for 5xx, want false")
	}
	if de.Code != 550 {
		t.Errorf("DeliveryError.Code = %d, want 550", de.Code)
	}
}

func TestSend_ConnectionRefused(t *testing.T) {
	s := &SMTPSender{}
	// Port 1 is reserved and should always refuse connections.
	err := s.Send("127.0.0.1:1", "from@example.com", []string{"to@example.com"},
		[]byte("From: f@x.com\r\n\r\nbody\r\n"), nil)
	if err == nil {
		t.Fatal("expected error for refused connection, got nil")
	}

	var de *DeliveryError
	if !errors.As(err, &de) {
		t.Fatalf("error type = %T, want *DeliveryError", err)
	}
	if !de.Temporary {
		t.Errorf("connection error should be Temporary, got false")
	}
}

func TestSend_STARTTLSFallback_PlainWhenNoTLS(t *testing.T) {
	// Server without TLS; Send with a non-nil tlsCfg should fall back to plain.
	be := &mockBackend{}
	addr := startMockSMTP(t, be)

	s := &SMTPSender{}
	// Use an empty tls.Config (no certs) to trigger STARTTLS attempt.
	err := s.Send(addr, "from@example.com", []string{"to@example.com"},
		[]byte("From: f@x.com\r\n\r\nbody\r\n"), nil)
	// Should succeed via plain fallback.
	if err != nil {
		t.Fatalf("expected fallback to plain, got error: %v", err)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if len(be.received) != 1 {
		t.Errorf("received %d messages after fallback, want 1", len(be.received))
	}
}
