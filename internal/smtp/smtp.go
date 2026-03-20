// Package smtp implements the inbound SMTP server (receiver).
package smtp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	sasl "github.com/emersion/go-sasl"
	gosmtp "github.com/emersion/go-smtp"

	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/message"
)

const maxMessageBytes = 32 << 20 // 32 MB

// MessageHandler is called once for every fully received message.
type MessageHandler func(msg *message.InboundMessage) error

// Server wraps two go-smtp servers: one for inbound MTA delivery (port 25)
// and one for client submission (port 587).
type Server struct {
	inbound    *gosmtp.Server
	submission *gosmtp.Server
}

// New creates a Server from cfg. Call Serve or ListenAndServe to start it.
func New(cfg config.Config, handler MessageHandler) (*Server, error) {
	domains := make(map[string]struct{}, len(cfg.Domains))
	for _, d := range cfg.Domains {
		domains[d.Name] = struct{}{}
	}

	var tlsCfg *tls.Config
	if cfg.Server.TLS.Enabled {
		cert, err := tls.LoadX509KeyPair(cfg.Server.TLS.Cert, cfg.Server.TLS.Key)
		if err != nil {
			return nil, fmt.Errorf("smtp: loading TLS certificate: %w", err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	inbound := gosmtp.NewServer(&inboundBackend{
		domains: domains,
		handler: handler,
	})
	inbound.Domain = cfg.Server.Hostname
	inbound.Addr = fmt.Sprintf(":%d", cfg.Server.InboundPort)
	inbound.MaxMessageBytes = maxMessageBytes
	inbound.TLSConfig = tlsCfg
	inbound.AllowInsecureAuth = true // port 25 doesn't require TLS

	submission := gosmtp.NewServer(&submissionBackend{
		domains: domains,
		handler: handler,
		users:   buildUserMap(cfg.Auth.Users),
	})
	submission.Domain = cfg.Server.Hostname
	submission.Addr = fmt.Sprintf(":%d", cfg.Server.SubmissionPort)
	submission.MaxMessageBytes = maxMessageBytes
	submission.TLSConfig = tlsCfg
	submission.AllowInsecureAuth = true // allow auth before TLS for clients without STARTTLS

	return &Server{inbound: inbound, submission: submission}, nil
}

// Serve accepts connections on the provided pre-bound listeners. It blocks
// until both servers stop (typically after Close is called).
func (s *Server) Serve(inboundL, submissionL net.Listener) error {
	errc := make(chan error, 2)
	go func() { errc <- s.inbound.Serve(inboundL) }()
	go func() { errc <- s.submission.Serve(submissionL) }()

	err1 := <-errc
	err2 := <-errc
	return errors.Join(err1, err2)
}

// ListenAndServe creates listeners from the configured addresses and calls Serve.
func (s *Server) ListenAndServe() error {
	errc := make(chan error, 2)
	go func() { errc <- s.inbound.ListenAndServe() }()
	go func() { errc <- s.submission.ListenAndServe() }()

	err1 := <-errc
	err2 := <-errc
	return errors.Join(err1, err2)
}

// Close shuts down both servers.
func (s *Server) Close() error {
	return errors.Join(s.inbound.Close(), s.submission.Close())
}

// buildUserMap converts the config user list to a username→hash map.
func buildUserMap(users []config.AuthUser) map[string][]byte {
	m := make(map[string][]byte, len(users))
	for _, u := range users {
		m[u.Username] = []byte(u.PasswordHash)
	}
	return m
}

// authenticate verifies username and password against the bcrypt hash map.
func authenticate(users map[string][]byte, username, password string) error {
	hash, ok := users[username]
	if !ok {
		// Use a dummy comparison to prevent timing-based username enumeration.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$placeholder"), []byte(password))
		return errors.New("smtp: authentication failed")
	}
	if err := bcrypt.CompareHashAndPassword(hash, []byte(password)); err != nil {
		return errors.New("smtp: authentication failed")
	}
	return nil
}

// domainOf extracts the domain part from an RFC 5321 address (user@domain).
// Returns empty string if the address contains no '@'.
func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return strings.ToLower(addr[i+1:])
	}
	return ""
}

// ---- inbound backend (port 25, no auth, domain validation) ----

type inboundBackend struct {
	domains map[string]struct{}
	handler MessageHandler
}

func (b *inboundBackend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &inboundSession{be: b}, nil
}

type inboundSession struct {
	be   *inboundBackend
	from string
	to   []string
}

func (s *inboundSession) Reset() {
	s.from = ""
	s.to = nil
}

func (s *inboundSession) Logout() error { return nil }

func (s *inboundSession) Mail(from string, _ *gosmtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *inboundSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	dom := domainOf(to)
	if _, ok := s.be.domains[dom]; !ok {
		return &gosmtp.SMTPError{
			Code:    550,
			Message: fmt.Sprintf("relay denied for domain %q", dom),
		}
	}
	s.to = append(s.to, to)
	return nil
}

func (s *inboundSession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	domain := ""
	if len(s.to) > 0 {
		domain = domainOf(s.to[0])
	}
	msg := &message.InboundMessage{
		ID:           message.NewID(),
		ReceivedAt:   time.Now(),
		Direction:    message.DirectionInbound,
		EnvelopeFrom: s.from,
		EnvelopeTo:   append([]string(nil), s.to...),
		Domain:       domain,
		RawMessage:   raw,
	}
	return s.be.handler(msg)
}

// ---- submission backend (port 587, AUTH required) ----

type submissionBackend struct {
	domains map[string]struct{}
	handler MessageHandler
	users   map[string][]byte
}

func (b *submissionBackend) NewSession(_ *gosmtp.Conn) (gosmtp.Session, error) {
	return &submissionSession{be: b}, nil
}

// submissionSession implements gosmtp.AuthSession.
type submissionSession struct {
	be            *submissionBackend
	from          string
	to            []string
	authenticated bool
}

func (s *submissionSession) Reset() {
	s.from = ""
	s.to = nil
	// authenticated state is preserved across resets in the same connection.
}

func (s *submissionSession) Logout() error { return nil }

func (s *submissionSession) AuthMechanisms() []string {
	return []string{sasl.Plain, sasl.Login}
}

func (s *submissionSession) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, username, password string) error {
			if err := authenticate(s.be.users, username, password); err != nil {
				return err
			}
			s.authenticated = true
			return nil
		}), nil
	case sasl.Login:
		return newLoginServer(func(username, password string) error {
			if err := authenticate(s.be.users, username, password); err != nil {
				return err
			}
			s.authenticated = true
			return nil
		}), nil
	default:
		return nil, gosmtp.ErrAuthUnknownMechanism
	}
}

func (s *submissionSession) Mail(from string, _ *gosmtp.MailOptions) error {
	if !s.authenticated {
		return &gosmtp.SMTPError{Code: 530, Message: "authentication required"}
	}
	s.from = from
	return nil
}

func (s *submissionSession) Rcpt(to string, _ *gosmtp.RcptOptions) error {
	s.to = append(s.to, to)
	return nil
}

func (s *submissionSession) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	domain := ""
	if len(s.to) > 0 {
		domain = domainOf(s.to[0])
	}
	msg := &message.InboundMessage{
		ID:           message.NewID(),
		ReceivedAt:   time.Now(),
		Direction:    message.DirectionOutbound,
		EnvelopeFrom: s.from,
		EnvelopeTo:   append([]string(nil), s.to...),
		Domain:       domain,
		RawMessage:   raw,
	}
	return s.be.handler(msg)
}

// ---- LOGIN SASL server ----

type loginServer struct {
	step     int
	username string
	auth     func(username, password string) error
}

func newLoginServer(auth func(username, password string) error) sasl.Server {
	return &loginServer{auth: auth}
}

// Next implements sasl.Server for the LOGIN mechanism.
// The go-smtp LOGIN client sends the username as the initial response,
// so step 0 handles both the "initial response provided" and "no IR" cases.
func (s *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	switch s.step {
	case 0:
		s.step++
		if len(response) > 0 {
			// Username arrived as initial response; jump straight to password.
			s.username = string(response)
			s.step++ // advance to step 2
			return []byte("Password:"), false, nil
		}
		return []byte("Username:"), false, nil
	case 1:
		s.username = string(response)
		s.step++
		return []byte("Password:"), false, nil
	case 2:
		err = s.auth(s.username, string(response))
		return nil, true, err
	default:
		return nil, true, sasl.ErrUnexpectedClientResponse
	}
}
