package message

import (
	"regexp"
	"strings"
	"testing"
)

// idPattern matches the expected ENV-<timestamp>-<hex> format.
var idPattern = regexp.MustCompile(`^ENV-\d{8}T\d{6}-[0-9a-f]{16}$`)

func TestNewID_Format(t *testing.T) {
	id := NewID()
	if !idPattern.MatchString(id) {
		t.Errorf("NewID() = %q, does not match expected format ENV-yyyymmddThhmmss-<16 hex chars>", id)
	}
}

func TestNewID_Prefix(t *testing.T) {
	id := NewID()
	if !strings.HasPrefix(id, "ENV-") {
		t.Errorf("NewID() = %q, expected prefix ENV-", id)
	}
}

func TestNewID_ThreeParts(t *testing.T) {
	id := NewID()
	parts := strings.Split(id, "-")
	// ENV - <timestamp> - <hex>  →  3 parts
	if len(parts) != 3 {
		t.Errorf("NewID() = %q, expected 3 dash-separated parts, got %d", id, len(parts))
	}
}

func TestNewID_Uniqueness(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for range n {
		id := NewID()
		if _, dup := seen[id]; dup {
			t.Fatalf("NewID() produced a duplicate after %d calls: %q", len(seen), id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewID_NoWhitespace(t *testing.T) {
	for range 100 {
		id := NewID()
		if strings.ContainsAny(id, " \t\r\n") {
			t.Errorf("NewID() = %q contains whitespace", id)
		}
	}
}

func TestNewID_HexSuffixLength(t *testing.T) {
	id := NewID()
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		t.Fatalf("unexpected parts: %v", parts)
	}
	if got := len(parts[2]); got != 16 {
		t.Errorf("hex suffix length = %d, want 16", got)
	}
}
