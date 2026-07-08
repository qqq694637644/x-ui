package xray_util

import "testing"

func TestStripDeprecatedKcpHeaderSeedRemovesHeaderAndSeed(t *testing.T) {
	input := "{\"network\":\"kcp\",\"kcpSettings\":{\"mtu\":1350,\"header\":{\"type\":\"srtp\"},\"seed\":\"legacy-seed\"}}"
	got, changed, err := StripDeprecatedKcpHeaderSeed(input)
	if err != nil {
		t.Fatalf("StripDeprecatedKcpHeaderSeed() error = %v", err)
	}
	if !changed {
		t.Fatal("StripDeprecatedKcpHeaderSeed() changed = false, want true")
	}
	if got == input {
		t.Fatal("StripDeprecatedKcpHeaderSeed() did not rewrite stream settings")
	}
	if err := RejectDeprecatedKcpHeaderSeed(input); err == nil {
		t.Fatal("RejectDeprecatedKcpHeaderSeed() accepted removed mKCP header/seed fields")
	}
}

func TestStripDeprecatedKcpHeaderSeedLeavesCurrentSettings(t *testing.T) {
	input := "{\"network\":\"kcp\",\"kcpSettings\":{\"mtu\":1350,\"tti\":20}}"
	got, changed, err := StripDeprecatedKcpHeaderSeed(input)
	if err != nil {
		t.Fatalf("StripDeprecatedKcpHeaderSeed() error = %v", err)
	}
	if changed {
		t.Fatal("StripDeprecatedKcpHeaderSeed() changed = true, want false")
	}
	if got != input {
		t.Fatalf("StripDeprecatedKcpHeaderSeed() = %s, want original", got)
	}
	if err := RejectDeprecatedKcpHeaderSeed(input); err != nil {
		t.Fatalf("RejectDeprecatedKcpHeaderSeed() error = %v, want nil", err)
	}
}
