package sandbox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PoolOptions configures a Pool.
type PoolOptions struct {
	// Options is the sandbox configuration applied to every pooled sandbox.
	Options Options

	// MinSize is the number of sandboxes pre-warmed at startup.
	// Defaults to 0.
	MinSize int

	// MaxSize is the maximum number of live sandboxes (idle + checked-out).
	// Defaults to 10.
	MaxSize int

	// IdleTTL is how long an idle sandbox may remain in the pool before
	// being evicted and closed. Zero disables idle eviction.
	IdleTTL time.Duration
}

type poolEntry struct {
	s         *Sandbox
	idleSince time.Time
}

// Pool manages a set of pre-warmed, reusable Sandbox instances.
//
// Acquire returns a ready-to-use sandbox; Release resets it and returns it
// to the pool. Sandboxes are created on demand up to MaxSize and pre-warmed
// to MinSize at startup so the first Acquire calls return instantly.
type Pool struct {
	opts PoolOptions
	pool chan poolEntry // buffered; capacity = MaxSize

	mu     sync.Mutex
	size   int  // total live sandboxes: idle (in pool) + checked-out
	closed bool

	once sync.Once
	done chan struct{}
}

// NewPool creates a Pool and synchronously pre-warms MinSize sandboxes so
// they are ready before the first Acquire call.
func NewPool(opts PoolOptions) *Pool {
	if opts.MaxSize <= 0 {
		opts.MaxSize = 10
	}
	if opts.MinSize < 0 {
		opts.MinSize = 0
	}
	if opts.MinSize > opts.MaxSize {
		opts.MinSize = opts.MaxSize
	}

	p := &Pool{
		opts: opts,
		pool: make(chan poolEntry, opts.MaxSize),
		done: make(chan struct{}),
	}

	// Pre-warm MinSize sandboxes synchronously — they must be ready before
	// the first Acquire() call (test requirement).
	for i := 0; i < opts.MinSize; i++ {
		s, err := New(opts.Options)
		if err != nil {
			break // best-effort; Acquire will create on demand
		}
		p.mu.Lock()
		p.size++
		p.mu.Unlock()
		p.pool <- poolEntry{s: s, idleSince: time.Now()}
	}

	if opts.IdleTTL > 0 {
		go p.evictLoop()
	}

	return p
}

// Acquire returns a sandbox from the pool.
//
// If the pool is empty and MaxSize has not been reached a new sandbox is
// created. If the pool is at capacity, Acquire blocks until a sandbox is
// released or ctx is cancelled.
func (p *Pool) Acquire(ctx context.Context) (*Sandbox, error) {
	for {
		// Fast path: grab from pool without blocking.
		select {
		case entry := <-p.pool:
			return entry.s, nil
		default:
		}

		// Try to reserve a new sandbox slot.
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("sandbox pool is closed")
		}
		canCreate := p.size < p.opts.MaxSize
		if canCreate {
			p.size++
		}
		p.mu.Unlock()

		if canCreate {
			s, err := New(p.opts.Options)
			if err != nil {
				p.mu.Lock()
				p.size--
				p.mu.Unlock()
				return nil, fmt.Errorf("sandbox pool: create sandbox: %w", err)
			}
			return s, nil
		}

		// Pool is at capacity; block until a release, close, or ctx cancel.
		select {
		case entry := <-p.pool:
			return entry.s, nil
		case <-p.done:
			return nil, fmt.Errorf("sandbox pool is closed")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Release resets s and returns it to the pool for reuse.
// If the pool is full or closed, s is closed and discarded.
func (p *Pool) Release(s *Sandbox) {
	s.Reset()

	p.mu.Lock()
	full := len(p.pool) >= p.opts.MaxSize
	closed := p.closed
	p.mu.Unlock()

	if closed || full {
		_ = s.Close()
		p.mu.Lock()
		p.size--
		p.mu.Unlock()
		return
	}

	select {
	case p.pool <- poolEntry{s: s, idleSince: time.Now()}:
	default:
		// Channel full due to a race; discard.
		_ = s.Close()
		p.mu.Lock()
		p.size--
		p.mu.Unlock()
	}
}

// Size returns the total number of live sandboxes (idle + checked-out).
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size
}

// Close shuts down the pool and closes all idle sandboxes.
// Sandboxes currently checked out are not closed; callers should still call
// Release on them (which will close them since the pool is already closed).
func (p *Pool) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		close(p.done)

		for {
			select {
			case entry := <-p.pool:
				_ = entry.s.Close()
				p.mu.Lock()
				p.size--
				p.mu.Unlock()
			default:
				return
			}
		}
	})
	return nil
}

func (p *Pool) evictLoop() {
	interval := p.opts.IdleTTL / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
			p.evictIdle()
		}
	}
}

func (p *Pool) evictIdle() {
	cutoff := time.Now().Add(-p.opts.IdleTTL)
	var keep []poolEntry

	// Drain the channel.
	for {
		select {
		case entry := <-p.pool:
			if entry.idleSince.After(cutoff) {
				keep = append(keep, entry)
			} else {
				_ = entry.s.Close()
				p.mu.Lock()
				p.size--
				p.mu.Unlock()
			}
		default:
			goto refill
		}
	}

refill:
	for _, entry := range keep {
		select {
		case p.pool <- entry:
		default:
			// Channel filled up during refill; discard the surplus.
			_ = entry.s.Close()
			p.mu.Lock()
			p.size--
			p.mu.Unlock()
		}
	}
}
