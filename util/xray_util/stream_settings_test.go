package xray_util

import "testing"

func TestValidateXray26327StreamSettingsRejectsLegacyKcpNetwork(t *testing.T) {
	input := "{\"network\":\"kcp\",\"kcpSettings\":{\"mtu\":1350,\"tti\":20}}"
	if err := ValidateXray26327StreamSettings(input); err == nil {
		t.Fatal("ValidateXray26327StreamSettings() accepted legacy network kcp")
	}
}

func TestValidateXray26327StreamSettingsRejectsLegacyKcpHeader(t *testing.T) {
	input := "{\"network\":\"mkcp\",\"kcpSettings\":{\"mtu\":1350,\"header\":{\"type\":\"srtp\"}}}"
	if err := ValidateXray26327StreamSettings(input); err == nil {
		t.Fatal("ValidateXray26327StreamSettings() accepted removed kcpSettings.header")
	}
}

func TestValidateXray26327StreamSettingsRejectsLegacyKcpSeed(t *testing.T) {
	input := "{\"network\":\"mkcp\",\"kcpSettings\":{\"mtu\":1350,\"seed\":\"legacy-seed\"}}"
	if err := ValidateXray26327StreamSettings(input); err == nil {
		t.Fatal("ValidateXray26327StreamSettings() accepted removed kcpSettings.seed")
	}
}

func TestValidateXray26327StreamSettingsAllowsMkcpBaseSettings(t *testing.T) {
	input := "{\"network\":\"mkcp\",\"kcpSettings\":{\"mtu\":1350,\"tti\":20,\"uplinkCapacity\":5,\"downlinkCapacity\":20,\"congestion\":false,\"readBufferSize\":1,\"writeBufferSize\":1}}"
	if err := ValidateXray26327StreamSettings(input); err != nil {
		t.Fatalf("ValidateXray26327StreamSettings() error = %v, want nil", err)
	}
}
