package server

import (
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Listen opens a TCP listener that logs every connection it accepts and, once
// the connection is closed, how long it lasted and how much data it carried.
// proto is only a label for the log lines ("smtp", "imap", ...).
func Listen(proto, addr string) (net.Listener, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &logListener{Listener: l, proto: proto}, nil
}

type logListener struct {
	net.Listener
	proto string
}

func (l *logListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	log := slog.With("proto", l.proto, "remote", c.RemoteAddr().String())
	log.Info("connection opened")
	return &logConn{Conn: c, log: log, start: time.Now()}, nil
}

type logConn struct {
	net.Conn
	log   *slog.Logger
	start time.Time
	rx    atomic.Int64
	tx    atomic.Int64
	once  sync.Once
}

func (c *logConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.rx.Add(int64(n))
	return n, err
}

func (c *logConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.tx.Add(int64(n))
	return n, err
}

func (c *logConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.log.Info("connection closed",
			"duration", time.Since(c.start),
			"rx_bytes", c.rx.Load(),
			"tx_bytes", c.tx.Load())
	})
	return err
}
