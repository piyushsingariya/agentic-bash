// Package limitwriter provides a Writer that stops accepting bytes once a cap
// is reached and fires a callback exactly once to signal the overrun.
package limitwriter

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// ErrLimitExceeded is returned by Write once the byte cap has been hit.
var ErrLimitExceeded = errors.New("output limit exceeded")

// Writer wraps an io.Writer and enforces a maximum byte count.
// Once the cap is reached it calls onLimit (exactly once) and returns
// ErrLimitExceeded on every subsequent Write.  Partial writes up to the cap
// are still forwarded to the underlying writer.
//
// Multiple Writers may share the same *atomic.Int64 counter to enforce a
// combined cap across stdout and stderr.
type Writer struct {
	w       io.Writer
	cap     int64
	counter *atomic.Int64 // shared across writers for combined cap
	once    *sync.Once    // shared across writers for one-shot callback
	onLimit func()        // called once when the cap is first exceeded
}

// New returns a Writer that calls onLimit when the cumulative byte count
// exceeds capBytes.  If capBytes <= 0 all writes pass through without counting.
//
// counter and once must be non-nil; create them with NewShared() to share a
// combined cap across multiple writers, or call NewSingle() for a standalone writer.
func New(w io.Writer, capBytes int64, counter *atomic.Int64, once *sync.Once, onLimit func()) *Writer {
	return &Writer{
		w:       w,
		cap:     capBytes,
		counter: counter,
		once:    once,
		onLimit: onLimit,
	}
}

// NewPair creates two Writers (e.g. stdout and stderr) that share a combined
// byte counter.  onLimit is called once when their combined output exceeds capBytes.
func NewPair(wOut, wErr io.Writer, capBytes int64, onLimit func()) (out, errW *Writer) {
	var ctr atomic.Int64
	once := &sync.Once{}
	out = New(wOut, capBytes, &ctr, once, onLimit)
	errW = New(wErr, capBytes, &ctr, once, onLimit)
	return
}

func (lw *Writer) Write(p []byte) (int, error) {
	if lw.cap <= 0 {
		return lw.w.Write(p)
	}
	after := lw.counter.Add(int64(len(p)))
	if after > lw.cap {
		lw.once.Do(func() {
			if lw.onLimit != nil {
				lw.onLimit()
			}
		})
		// Write only the bytes that fit within the cap.
		fits := lw.cap - (after - int64(len(p)))
		if fits > 0 {
			_, _ = lw.w.Write(p[:fits])
		}
		return len(p), ErrLimitExceeded
	}
	return lw.w.Write(p)
}

