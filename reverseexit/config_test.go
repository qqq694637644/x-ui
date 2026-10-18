package reverseexit

import (
	"context"
	"testing"
	"time"
)

func TestExitRequiresWARPAndLoopbackOnly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "a-test-token-longer-than-16"
	if err := cfg.Validate(); err == nil {
		t.Fatal("WARP is mandatory")
	}
	cfg.WARPAddr = "127.0.0.1:40000"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"0.0.0.0:40000", "8.8.8.8:1080", "localhost:40000"} {
		wrong := cfg
		wrong.WARPAddr = addr
		if err := wrong.Validate(); err == nil {
			t.Fatalf("accepted a WARP endpoint not bound to literal loopback: %q", addr)
		}
	}
	wrong := cfg
	wrong.UDPBindAddr = wrong.UDPPeerAddr
	if err := wrong.Validate(); err == nil {
		t.Fatal("accepted identical UDP endpoints")
	}
	wrong = cfg
	wrong.WARPUsername = "username"
	if err := wrong.Validate(); err == nil {
		t.Fatal("accepted missing WARP password")
	}
	wrong.WARPPassword = "password"
	if err := wrong.Validate(); err != nil {
		t.Fatal(err)
	}
	// Invalid configuration refuses to run without attempting a destination.
	if err := Run(context.Background(), Config{Token: cfg.Token,
		RetryInterval: time.Second}, nil); err == nil {
		t.Fatal("accepted a missing WARP proxy")
	}
}
