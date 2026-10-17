package reverseexit

// Bounded RSX2/RSX3 frame writer: one owner of the underlying TCP stream,
// control-first scheduling, round-robin DATA per session, and explicit
// backpressure. A FIN stays behind pending DATA from the same session.
import (
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

const (
	rsxControlQueueLimit = 128
	rsxDataQueueLimit    = 256
	rsxPerSessionLimit   = 8
	rsxSlowThreshold     = 100 * time.Millisecond
)

type frameRequest struct {
	value   frame
	queued  time.Time
	replied chan error
}

type fairWriter struct {
	conn net.Conn

	mu       sync.Mutex
	changed  *sync.Cond
	controls []*frameRequest
	data     map[uint32][]*frameRequest
	turns    []uint32
	numData  int
	closed   bool
	failure  error
	done     chan struct{}

	bytes    uint64
	maxQueue time.Duration
	maxWrite time.Duration
}

func newFairWriter(conn net.Conn) *fairWriter {
	w := &fairWriter{conn: conn, data: make(map[uint32][]*frameRequest), done: make(chan struct{})}
	w.changed = sync.NewCond(&w.mu)
	go w.run()
	return w
}

func (w *fairWriter) send(f frame) error {
	req := &frameRequest{value: f, queued: time.Now(), replied: make(chan error, 1)}
	dataLane := f.kind == tcpData || f.kind == tcpFIN
	w.mu.Lock()
	for !w.closed {
		if dataLane {
			if w.numData < rsxDataQueueLimit && len(w.data[f.id]) < rsxPerSessionLimit {
				break
			}
		} else if len(w.controls) < rsxControlQueueLimit {
			break
		}
		w.changed.Wait()
	}
	if w.closed {
		err := w.failure
		w.mu.Unlock()
		if err == nil {
			return net.ErrClosed
		}
		return err
	}
	if dataLane {
		if len(w.data[f.id]) == 0 {
			w.turns = append(w.turns, f.id)
		}
		w.data[f.id] = append(w.data[f.id], req)
		w.numData++
	} else {
		w.controls = append(w.controls, req)
	}
	w.changed.Signal()
	w.mu.Unlock()
	return <-req.replied
}

func (w *fairWriter) pop() *frameRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	for !w.closed && len(w.controls) == 0 && w.numData == 0 {
		w.changed.Wait()
	}
	if w.closed {
		return nil
	}
	var req *frameRequest
	if len(w.controls) > 0 {
		req = w.controls[0]
		w.controls[0] = nil
		w.controls = w.controls[1:]
	} else {
		sid := w.turns[0]
		w.turns = w.turns[1:]
		queue := w.data[sid]
		req = queue[0]
		queue[0] = nil
		queue = queue[1:]
		w.numData--
		if len(queue) == 0 {
			delete(w.data, sid)
		} else {
			w.data[sid] = queue
			w.turns = append(w.turns, sid)
		}
	}
	w.changed.Broadcast()
	return req
}

func (w *fairWriter) run() {
	defer close(w.done)
	for {
		req := w.pop()
		if req == nil {
			return
		}
		queued := time.Since(req.queued)
		start := time.Now()
		err := w.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if err == nil {
			err = writeFrame(w.conn, req.value)
		}
		written := time.Since(start)
		w.mu.Lock()
		if queued > w.maxQueue {
			w.maxQueue = queued
		}
		if written > w.maxWrite {
			w.maxWrite = written
		}
		if err == nil {
			w.bytes += uint64(len(req.value.data))
		}
		w.mu.Unlock()
		if queued > rsxSlowThreshold || written > rsxSlowThreshold {
			log.Printf("RSX metric frame_write kind=%d sid=%d queued_ms=%d write_ms=%d",
				req.value.kind, req.value.id, queued.Milliseconds(), written.Milliseconds())
		}
		req.replied <- err
		if err != nil {
			// Closing the stream wakes blocked readers and frees all producers.
			_ = w.conn.Close()
			w.finish(err)
			return
		}
	}
}

func (w *fairWriter) finish(err error) {
	if err == nil {
		err = net.ErrClosed
	}
	w.mu.Lock()
	w.closed = true
	w.failure = err
	pending := w.controls
	for _, lane := range w.data {
		pending = append(pending, lane...)
	}
	w.controls = nil
	w.data = make(map[uint32][]*frameRequest)
	w.turns = nil
	w.numData = 0
	w.changed.Broadcast()
	w.mu.Unlock()
	for _, req := range pending {
		req.replied <- err
	}
}

func (w *fairWriter) close() {
	w.mu.Lock()
	wasClosed := w.closed
	w.mu.Unlock()
	if !wasClosed {
		_ = w.conn.Close()
		// Important: wake idle writer while preserving pending producer errors.
		w.finish(errors.New("RSX writer closed"))
	}
	<-w.done
	w.mu.Lock()
	log.Printf("RSX metric writer_summary bytes=%d queue_max_ms=%d write_max_ms=%d",
		w.bytes, w.maxQueue.Milliseconds(), w.maxWrite.Milliseconds())
	w.mu.Unlock()
}
