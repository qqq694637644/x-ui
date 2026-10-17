package reverseexit

import (
	"net"
	"sync"
	"testing"
	"time"
)

type notifiedWriteConn struct {
	net.Conn
	once    sync.Once
	started chan struct{}
}

func (c *notifiedWriteConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func waitWriterQueue(t *testing.T, w *fairWriter, wantControl, wantData int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		c, d := len(w.controls), w.numData
		w.mu.Unlock()
		if c >= wantControl && d >= wantData {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("writer did not queue %d control and %d data frames", wantControl, wantData)
}

func TestRSXWriterPrioritizesControlAndPreservesSessionFIN(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	watch := &notifiedWriteConn{Conn: left, started: make(chan struct{})}
	w := newFairWriter(watch)
	defer w.close()
	first := make(chan error, 1)
	go func() { first <- w.send(frame{tcpData, 1, []byte("first")}) }()
	select {
	case <-watch.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first frame was never selected by the writer")
	}
	// The first DATA frame is blocked in net.Pipe's write until a reader
	// drains it. This gives us deterministic backlog scheduling.
	second := make(chan error, 1)
	go func() { second <- w.send(frame{tcpData, 1, []byte("second")}) }()
	waitWriterQueue(t, w, 0, 1)
	fin := make(chan error, 1)
	go func() { fin <- w.send(frame{tcpFIN, 1, nil}) }()
	waitWriterQueue(t, w, 0, 2)
	control := make(chan error, 1)
	go func() { control <- w.send(frame{tcpWindow, 2, []byte{0, 1}}) }()
	waitWriterQueue(t, w, 1, 2)

	_ = right.SetReadDeadline(time.Now().Add(4 * time.Second))
	want := []byte{tcpData, tcpWindow, tcpData, tcpFIN}
	for i, kind := range want {
		actual, err := readFrame(right)
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if actual.kind != kind {
			t.Fatalf("frame %d: want %d got %d", i, kind, actual.kind)
		}
	}
	for _, result := range []<-chan error{first, second, fin, control} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("writer producer blocked after frame delivery")
		}
	}
}

func TestRSXWriterRejectsPendingFramesOnShutdown(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	w := newFairWriter(left)
	one := make(chan error, 1)
	go func() { one <- w.send(frame{tcpData, 1, []byte("blocked")}) }()
	time.Sleep(15 * time.Millisecond)
	more := make(chan error, 1)
	go func() { more <- w.send(frame{tcpData, 1, []byte("queued")}) }()
	waitWriterQueue(t, w, 0, 1)
	w.close()
	for _, result := range []<-chan error{one, more} {
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("shutdown lost queued writer error")
			}
		case <-time.After(time.Second):
			t.Fatal("writer producer deadlocked on shutdown")
		}
	}
}
