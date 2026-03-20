package journal

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	gomail "github.com/emersion/go-message"
	"github.com/phoenix66/envoy/internal/config"
	"github.com/phoenix66/envoy/internal/message"
)

// synthetic original message used across all tests.
const rawOriginal = "From: sender@example.com\r\n" +
	"To: recipient@example.com\r\n" +
	"Subject: Quarterly Report\r\n" +
	"Message-Id: <abc123@example.com>\r\n" +
	"Date: Mon, 01 Jan 2024 10:00:00 +0000\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Hello, this is the message body.\r\n"

var testArchiveCfg = config.ArchiveConfig{
	SMTPHost:    "archive.example.com",
	SMTPPort:    7700,
	JournalFrom: "journal@example.com",
	JournalTo:   "archive@example.com",
	TLS:         config.TLSConfig{Enabled: true},
}

var testMsg = &message.InboundMessage{
	ID:           "ENV-20240101T100000-deadbeef01234567",
	ReceivedAt:   time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
	Direction:    message.DirectionInbound,
	EnvelopeFrom: "sender@example.com",
	EnvelopeTo:   []string{"recipient@example.com", "cc@example.com"},
	Domain:       "example.com",
	RawMessage:   []byte(rawOriginal),
}

// build is a test helper that calls Build and fails the test on error.
func build(t *testing.T) []byte {
	t.Helper()
	b := New(testArchiveCfg)
	out, err := b.Build(testMsg)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	return out
}

// parseOuter parses the built output as a MIME entity.
func parseOuter(t *testing.T, data []byte) *gomail.Entity {
	t.Helper()
	entity, err := gomail.Read(bytes.NewReader(data))
	if err != nil && !gomail.IsUnknownCharset(err) && !gomail.IsUnknownEncoding(err) {
		t.Fatalf("output is not valid MIME: %v", err)
	}
	return entity
}

// parsedPart holds a multipart child entity and its fully buffered body.
// The body must be buffered before calling NextPart, as the underlying
// multipart stream reader advances past each part boundary on each call.
type parsedPart struct {
	entity *gomail.Entity
	body   []byte
}

// collectParts reads all multipart children from entity, buffering each
// body before advancing to the next part.
func collectParts(t *testing.T, entity *gomail.Entity) []parsedPart {
	t.Helper()
	mr := entity.MultipartReader()
	if mr == nil {
		t.Fatal("outer entity is not multipart")
	}
	var parts []parsedPart
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !gomail.IsUnknownCharset(err) && !gomail.IsUnknownEncoding(err) {
			t.Fatalf("reading part: %v", err)
		}
		body, err := io.ReadAll(p.Body)
		if err != nil {
			t.Fatalf("reading part body: %v", err)
		}
		parts = append(parts, parsedPart{entity: p, body: body})
	}
	return parts
}

// --- tests ---

func TestBuild_ValidMIME(t *testing.T) {
	data := build(t)
	_ = parseOuter(t, data) // fails the test internally if invalid
}

func TestBuild_TwoParts(t *testing.T) {
	parts := collectParts(t, parseOuter(t, build(t)))
	if got := len(parts); got != 2 {
		t.Errorf("expected 2 parts, got %d", got)
	}
}

func TestBuild_OuterHeaders(t *testing.T) {
	entity := parseOuter(t, build(t))
	h := entity.Header

	cases := []struct{ name, want string }{
		{"From", testArchiveCfg.JournalFrom},
		{"To", testArchiveCfg.JournalTo},
		{"Subject", "Journal Report"},
		{"X-Envoy-Id", testMsg.ID},
		{"X-Envoy-Direction", string(testMsg.Direction)},
	}
	for _, tc := range cases {
		if got := h.Get(tc.name); got != tc.want {
			t.Errorf("header %q = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuild_OuterDateHeader(t *testing.T) {
	entity := parseOuter(t, build(t))
	dateHdr := entity.Header.Get("Date")
	if dateHdr == "" {
		t.Fatal("Date header is missing")
	}
	// Must contain the year at minimum.
	if !strings.Contains(dateHdr, "2024") {
		t.Errorf("Date header %q does not contain expected year 2024", dateHdr)
	}
}

func TestBuild_OuterContentTypeIsMultipartReport(t *testing.T) {
	entity := parseOuter(t, build(t))
	ct, params, err := entity.Header.ContentType()
	if err != nil {
		t.Fatalf("parsing Content-Type: %v", err)
	}
	if ct != "multipart/report" {
		t.Errorf("Content-Type = %q, want multipart/report", ct)
	}
	if params["report-type"] != "delivery-status" {
		t.Errorf("report-type = %q, want delivery-status", params["report-type"])
	}
	if params["boundary"] == "" {
		t.Error("Content-Type boundary parameter is missing")
	}
}

func TestBuild_Part1IsTextPlain(t *testing.T) {
	parts := collectParts(t, parseOuter(t, build(t)))
	ct, _, err := parts[0].entity.Header.ContentType()
	if err != nil {
		t.Fatalf("parsing part 1 Content-Type: %v", err)
	}
	if ct != "text/plain" {
		t.Errorf("part 1 Content-Type = %q, want text/plain", ct)
	}
}

func TestBuild_Part1ContainsEnvelopeMetadata(t *testing.T) {
	parts := collectParts(t, parseOuter(t, build(t)))
	text := string(parts[0].body)

	checks := []struct{ label, want string }{
		{"EnvelopeFrom", testMsg.EnvelopeFrom},
		{"EnvelopeTo[0]", testMsg.EnvelopeTo[0]},
		{"EnvelopeTo[1]", testMsg.EnvelopeTo[1]},
		{"Direction", string(testMsg.Direction)},
		{"Journal-Record-Id", testMsg.ID},
		// Extracted from original message headers.
		{"orig Subject", "Quarterly Report"},
		{"orig Message-Id", "<abc123@example.com>"},
	}
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("part 1 body missing %s (%q)\nbody:\n%s", c.label, c.want, text)
		}
	}
}

func TestBuild_Part2IsMessageRFC822(t *testing.T) {
	parts := collectParts(t, parseOuter(t, build(t)))
	ct, _, err := parts[1].entity.Header.ContentType()
	if err != nil {
		t.Fatalf("parsing part 2 Content-Type: %v", err)
	}
	if ct != "message/rfc822" {
		t.Errorf("part 2 Content-Type = %q, want message/rfc822", ct)
	}
}

func TestBuild_Part2BodyIdenticalToInput(t *testing.T) {
	parts := collectParts(t, parseOuter(t, build(t)))
	if !bytes.Equal(parts[1].body, testMsg.RawMessage) {
		t.Errorf("part 2 body is not byte-for-byte identical to RawMessage\ngot  (%d bytes): %q\nwant (%d bytes): %q",
			len(parts[1].body), parts[1].body, len(testMsg.RawMessage), testMsg.RawMessage)
	}
}

func TestBuild_MultipleRecipientsJoined(t *testing.T) {
	parts := collectParts(t, parseOuter(t, build(t)))
	text := string(parts[0].body)
	// Both recipients should appear on the same Recipients line.
	if !strings.Contains(text, "recipient@example.com; cc@example.com") {
		t.Errorf("part 1 body missing joined recipients\nbody:\n%s", text)
	}
}

func TestBuild_OutboundDirection(t *testing.T) {
	outboundMsg := *testMsg
	outboundMsg.Direction = message.DirectionOutbound

	b := New(testArchiveCfg)
	data, err := b.Build(&outboundMsg)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	entity := parseOuter(t, data)
	if got := entity.Header.Get("X-Envoy-Direction"); got != "outbound" {
		t.Errorf("X-Envoy-Direction = %q, want outbound", got)
	}

	parts := collectParts(t, entity)
	if !strings.Contains(string(parts[0].body), "outbound") {
		t.Errorf("part 1 body does not contain 'outbound'\nbody:\n%s", string(parts[0].body))
	}
}

func TestBuild_MissingOriginalHeaders(t *testing.T) {
	// A minimal raw message with no Subject/Message-Id/Date.
	bare := []byte("From: a@b.com\r\n\r\nBody.\r\n")
	msg := *testMsg
	msg.RawMessage = bare

	b := New(testArchiveCfg)
	_, err := b.Build(&msg)
	if err != nil {
		t.Fatalf("Build() should not fail on missing original headers, got: %v", err)
	}
}
