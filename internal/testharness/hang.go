package testharness

import (
	"net"
	"sync"
)

// gate blocks waiters while it is shut.
type gate struct {
	m sync.Mutex
	// ch is closed while the gate is open.
	ch chan struct{}
}

func newGate() *gate {
	ch := make(chan struct{})
	close(ch)

	return &gate{ch: ch}
}

func (g *gate) shut() {
	g.m.Lock()
	defer g.m.Unlock()

	select {
	case <-g.ch:
		g.ch = make(chan struct{})
	default:
	}
}

func (g *gate) open() {
	g.m.Lock()
	defer g.m.Unlock()

	select {
	case <-g.ch:
	default:
		close(g.ch)
	}
}

func (g *gate) wait() <-chan struct{} {
	g.m.Lock()
	defer g.m.Unlock()

	return g.ch
}

// hangListener hands out conns that withhold what they read while the gate is
// shut, like a host that stopped answering but still accepts connections. A
// withheld conn is closed once the gate opens.
type hangListener struct {
	net.Listener
	gate *gate
}

func (l *hangListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	return &hangConn{Conn: c, gate: l.gate, closed: make(chan struct{})}, nil
}

type hangConn struct {
	net.Conn
	gate *gate

	closed    chan struct{}
	closeOnce sync.Once
}

func (c *hangConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)

	open := c.gate.wait()
	select {
	case <-open:
		return n, err
	default:
	}

	// withheld: by the time the gate opens the caller gave up, so drop it
	select {
	case <-open:
		c.Close()
	case <-c.closed:
	}

	return 0, net.ErrClosed
}

func (c *hangConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })

	return c.Conn.Close()
}
