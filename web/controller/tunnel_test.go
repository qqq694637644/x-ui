package controller

import "testing"

func TestRejectDeprecatedTunnelFieldsRejectsLegacyJSON(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("{\"kcpHeaderType\":\"srtp\"}"), "")
	if err == nil {
		t.Fatal("rejectDeprecatedTunnelFields() accepted removed kcpHeaderType JSON field")
	}
}

func TestRejectDeprecatedTunnelFieldsRejectsLegacyForm(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("kcpSeed=legacy-seed"), "")
	if err == nil {
		t.Fatal("rejectDeprecatedTunnelFields() accepted removed kcpSeed form field")
	}
}

func TestRejectDeprecatedTunnelFieldsRejectsLegacyQuery(t *testing.T) {
	err := rejectDeprecatedTunnelFields(nil, "kcpSeed=legacy-seed")
	if err == nil {
		t.Fatal("rejectDeprecatedTunnelFields() accepted removed kcpSeed query field")
	}
}

func TestRejectDeprecatedTunnelFieldsAllowsCurrentFields(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("{\"kcpMtu\":1350,\"kcpTti\":20}"), "")
	if err != nil {
		t.Fatalf("rejectDeprecatedTunnelFields() error = %v, want nil", err)
	}
}
