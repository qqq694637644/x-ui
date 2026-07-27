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

func TestNormalizeUUIDMatchesXrayShortIDAlgorithm(t *testing.T) {
	actual, err := NormalizeUUID("my-home-xray")
	if err != nil {
		t.Fatalf("NormalizeUUID(short ID) error = %v", err)
	}
	const expected = "717ca3f3-97cd-589b-b805-3acd24b97366"
	if actual != expected {
		t.Fatalf("NormalizeUUID(short ID) = %q, want Xray-core result %q", actual, expected)
	}
}

func TestNormalizeUUIDRejectsMalformedValue(t *testing.T) {
	for _, input := range []string{
		"",
		"1234567890123456789012345678901",
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
