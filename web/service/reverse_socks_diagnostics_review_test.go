package service

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestDiagnosticsTargetBenchmarkRequiresCompleteBody(t *testing.T) {
	test := func(label string, body io.Reader, expected int64, wantBytes int64, wantState string, wantError error) {
		t.Helper()
		n, state, err := copyTargetExactly(io.Discard, body, expected)
		if n != wantBytes || state != wantState || (wantError == nil && err != nil) || (wantError != nil && !errors.Is(err, wantError)) {
			t.Fatalf("%s: copied=%d state=%s err=%v, expected=%d %s %v", label, n, state, err, wantBytes, wantState, wantError)
		}
	}
	test("short 1MB of 50MB", strings.NewReader(strings.Repeat("z", 1_000_000)), 50_000_000, 1_000_000, "short_body", io.ErrUnexpectedEOF)
	test("complete 1MB", strings.NewReader(strings.Repeat("z", 1_000_000)), 1_000_000, 1_000_000, "complete", nil)
	test("interrupted", iotest.ErrReader(io.ErrClosedPipe), 1_000_000, 0, "interrupted", io.ErrClosedPipe)
}

func TestDiagnosticsBothSideFailureRatesStayIndependent(t *testing.T) {
	now := time.Now()
	var goWindow, nodeWindow []diagnosticFailureSample
	var rate *float64
	goWindow, rate = failureWindow(goWindow, now, 20, 0)
	if rate == nil || *rate != 0 {
		t.Fatal("Go Agent healthy rate incorrectly reported")
	}
	nodeWindow, rate = failureWindow(nodeWindow, now, 20, 11)
	if rate == nil || *rate != 55 {
		t.Fatal("Windows Python failures omitted")
	}
	goWindow, rate = failureWindow(goWindow, now.Add(6*time.Minute), 20, 1)
	if rate == nil || *rate != 5 || len(goWindow) != 1 {
		t.Fatal("Go rate did not drop stale samples")
	}
	nodeWindow, rate = failureWindow(nodeWindow, now.Add(6*time.Minute), 20, 4)
	if rate == nil || *rate != 20 || len(nodeWindow) != 1 {
		t.Fatal("Python rate did not drop stale samples")
	}
}
