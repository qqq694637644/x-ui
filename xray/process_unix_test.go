//go:build !windows

package xray

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFakeXray(t *testing.T, body string) func() {
	t.Helper()
	path := GetBinaryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old, readErr := os.ReadFile(path)
	oldInfo, statErr := os.Stat(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return func() {
		_ = os.Remove(GetConfigPath())
		if readErr == nil {
			mode := os.FileMode(0o755)
			if statErr == nil {
				mode = oldInfo.Mode()
			}
			_ = os.WriteFile(path, old, mode)
		} else {
			_ = os.Remove(path)
		}
	}
}

func TestProcessStartReportsImmediateExit(t *testing.T) {
	restoreBinary := writeFakeXray(t, `
if [ "$1" = "-version" ]; then
  echo "Xray 26.3.27"
  exit 0
fi
echo "bind failed" >&2
exit 42`)
	defer restoreBinary()

	oldGrace := processStartupGrace
	processStartupGrace = 200 * time.Millisecond
	defer func() { processStartupGrace = oldGrace }()

	process := NewProcess(&Config{})
	if err := process.Start(); err == nil {
		t.Fatal("Start() succeeded for a process that exited immediately")
	}
	if process.IsRunning() {
		t.Fatal("process is reported as running after immediate exit")
	}
}

func TestProcessStartAndStopWaitForRealLifecycle(t *testing.T) {
	restoreBinary := writeFakeXray(t, `
if [ "$1" = "-version" ]; then
  echo "Xray 26.3.27"
  exit 0
fi
exec sleep 30`)
	defer restoreBinary()

	oldGrace := processStartupGrace
	oldStopTimeout := processStopTimeout
	processStartupGrace = 100 * time.Millisecond
	processStopTimeout = 2 * time.Second
	defer func() {
		processStartupGrace = oldGrace
		processStopTimeout = oldStopTimeout
	}()

	process := NewProcess(&Config{})
	if err := process.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !process.IsRunning() {
		t.Fatal("process is not reported as running after startup grace")
	}
	if err := process.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if process.IsRunning() {
		t.Fatal("process is still reported as running after Stop()")
	}
}
