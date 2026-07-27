package model

import "testing"

func TestNormalizeUUIDCanonicalizesAcceptedForms(t *testing.T) {
	const canonical = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	for _, input := range []string{
		"AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"  AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA  ",
	} {
		actual, err := NormalizeUUID(input)
		if err != nil {
			t.Fatalf("NormalizeUUID(%q) error = %v", input, err)
		}
		if actual != canonical {
			t.Fatalf("NormalizeUUID(%q) = %q, want %q", input, actual, canonical)
		}
	}
}

func TestNormalizeUUIDRejectsMalformedValue(t *testing.T) {
	for _, input := range []string{
		"not-a-uuid",
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa",
		"aaaaaaaaaaaa-aaaa-aaaa-aaaaaaaaaaaa",
	} {
		if _, err := NormalizeUUID(input); err == nil {
			t.Fatalf("NormalizeUUID(%q) unexpectedly succeeded", input)
		}
	}
}

func TestReverseDomainUsesCanonicalUUID(t *testing.T) {
	tunnel := &Tunnel{UUID: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if got, want := tunnel.ReverseDomain(), "reverse-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.xui.internal"; got != want {
		t.Fatalf("ReverseDomain() = %q, want %q", got, want)
	}
}
