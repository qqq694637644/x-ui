package service

import "testing"

func TestSetToNeedRestartFailsManagedStateClosedUntilXrayRestart(t *testing.T) {
	setManagedStateHealthy(true)
	defer setManagedStateHealthy(true)

	service := &XrayService{}
	service.SetToNeedRestart()
	if isManagedStateHealthy() {
		t.Fatal("managed state stayed healthy while Xray restart was pending")
	}
	if !service.IsNeedRestartAndSetFalse() {
		t.Fatal("Xray restart flag was not set")
	}
	if isManagedStateHealthy() {
		t.Fatal("clearing the restart queue unexpectedly marked managed state healthy")
	}
}
