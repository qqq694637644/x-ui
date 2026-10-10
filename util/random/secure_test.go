package random

import (
	"strings"
	"testing"
)

func TestSecureLowerSeqUsesAllowedAlphabet(t *testing.T) {
	value, err := SecureLowerSeq(64)
	if err != nil {
		t.Fatalf("SecureLowerSeq() error = %v", err)
	}
	if len(value) != 64 {
		t.Fatalf("length = %d, want 64", len(value))
	}
	for _, ch := range value {
		if !strings.ContainsRune(secureLowerNum, ch) {
			t.Fatalf("unexpected rune %q", ch)
		}
	}
}

func TestSecureTokenProducesDistinctValues(t *testing.T) {
	first, err := SecureToken(32)
	if err != nil {
		t.Fatalf("SecureToken(first) error = %v", err)
	}
	second, err := SecureToken(32)
	if err != nil {
		t.Fatalf("SecureToken(second) error = %v", err)
	}
	if first == second {
		t.Fatal("two secure tokens unexpectedly matched")
	}
	if len(first) < 40 {
		t.Fatalf("encoded token too short: %d", len(first))
	}
}
