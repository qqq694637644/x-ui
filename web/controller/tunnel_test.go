package controller

import "testing"

func TestRejectDeprecatedTunnelFieldsAllowsKcpHeaderTypeJSON(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("{\"kcpHeaderType\":\"srtp\"}"), "")
	if err != nil {
		t.Fatalf("rejectDeprecatedTunnelFields() error = %v, want nil", err)
	}
}

func TestRejectDeprecatedTunnelFieldsAllowsKcpSeedForm(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("kcpSeed=legacy-seed"), "")
	if err != nil {
		t.Fatalf("rejectDeprecatedTunnelFields() error = %v, want nil", err)
	}
}

func TestRejectDeprecatedTunnelFieldsAllowsKcpSeedQuery(t *testing.T) {
	err := rejectDeprecatedTunnelFields(nil, "kcpSeed=legacy-seed")
	if err != nil {
		t.Fatalf("rejectDeprecatedTunnelFields() error = %v, want nil", err)
	}
}

func TestRejectDeprecatedTunnelFieldsAllowsCurrentFields(t *testing.T) {
	err := rejectDeprecatedTunnelFields([]byte("{\"kcpMtu\":1350,\"kcpTti\":20}"), "")
	if err != nil {
		t.Fatalf("rejectDeprecatedTunnelFields() error = %v, want nil", err)
	}
}
