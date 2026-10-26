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
func waitWriterQueue(t *testing.T, w *fairWriter, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		have := len(w.controls)
		w.mu.Unlock()
		if have >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("writer did not queue %d control frames", count)
}
func TestRSXWriterSerializesOnlyControlFrames(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	watch := &notifiedWriteConn{Conn: left, started: make(chan struct{})}
	w := newFairWriter(watch)
	defer w.close()
	first := make(chan error, 1)
	go func() { first <- w.send(frame{openTCP, 1, []byte("destination")}) }()
	select {
	case <-watch.started:
	case <-time.After(2 * time.Second):
		t.Fatal("writer not started")
	}
	second := make(chan error, 1)
	go func() { second <- w.send(frame{openUDP, 2, nil}) }()
	waitWriterQueue(t, w, 1)
	third := make(chan error, 1)
	go func() { third <- w.send(frame{ping, 0, []byte("capability")}) }()
	waitWriterQueue(t, w, 2)
	_ = right.SetReadDeadline(time.Now().Add(4 * time.Second))
	for i, kind := range []byte{openTCP, openUDP, ping} {
		actual, err := readFrame(right)
		if err != nil || actual.kind != kind {
			t.Fatalf("frame %d wanted %d, got %+v err=%v", i, kind, actual, err)
		}
	}
	for _, result := range []<-chan error{first, second, third} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("writer blocked")
		}
	}
}
func TestRSXWriterRejectsPendingControlFramesOnShutdown(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	w := newFairWriter(left)
	one := make(chan error, 1)
	go func() { one <- w.send(frame{openTCP, 1, nil}) }()
	time.Sleep(15 * time.Millisecond)
	more := make(chan error, 1)
	go func() { more <- w.send(frame{openUDP, 2, nil}) }()
	waitWriterQueue(t, w, 1)
	w.close()
	for _, result := range []<-chan error{one, more} {
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("shutdown lost error")
			}
		case <-time.After(time.Second):
			t.Fatal("deadlocked")
		}
	}
}
