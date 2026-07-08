package controller

import "testing"

func TestRejectDeprecatedTunnelFieldsRejectsKcpHeaderTypeJSON(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("{\"kcpHeaderType\":\"srtp\"}"), "")
	if err == nil {
		t.Fatal("rejectDeprecatedTunnelFields() accepted deprecated kcpHeaderType JSON field")
	}
}

func TestRejectDeprecatedTunnelFieldsRejectsKcpSeedForm(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("kcpSeed=legacy-seed"), "")
	if err == nil {
		t.Fatal("rejectDeprecatedTunnelFields() accepted deprecated kcpSeed form field")
	}
}

func TestRejectDeprecatedTunnelFieldsRejectsKcpSeedQuery(t *testing.T) {
	err := rejectDeprecatedTunnelFields(nil, "kcpSeed=legacy-seed")
	if err == nil {
		t.Fatal("rejectDeprecatedTunnelFields() accepted deprecated kcpSeed query field")
	}
}

func TestRejectDeprecatedTunnelFieldsAllowsCurrentFields(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("{\"kcpMtu\":1350,\"kcpTti\":20}"), "")
	if err != nil {
		t.Fatalf("rejectDeprecatedTunnelFields() error = %v, want nil", err)
	}
}
