package main

import (
	"bufio"
	"bytes"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

type tradeDrainConn struct {
	net.Conn
	closed   bool
	deadline time.Time
}

func (c *tradeDrainConn) Close() error                      { c.closed = true; return nil }
func (c *tradeDrainConn) SetReadDeadline(t time.Time) error { c.deadline = t; return nil }

type tradeDrainWriter struct {
	*httptest.ResponseRecorder
	conn   *tradeDrainConn
	buffer *bufio.ReadWriter
}

func (w *tradeDrainWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, w.buffer, nil
}

func TestTradeDrainBoundedAndCloses(t *testing.T) {
	for _, size := range []int{0, 63836, 65536, 70000} {
		input := bytes.NewReader(make([]byte, size))
		reader := bufio.NewReader(input)
		conn := &tradeDrainConn{}
		w := &tradeDrainWriter{httptest.NewRecorder(), conn, bufio.NewReadWriter(reader, bufio.NewWriter(&bytes.Buffer{}))}
		started := time.Now()
		drainTradeUploadBeforeClose(w, httptest.NewRequest("POST", "/v1/validate", nil))
		remaining := reader.Buffered() + input.Len()
		want := max(0, size-65536)
		if remaining != want || !conn.closed {
			t.Fatalf("size=%d remaining=%d want=%d closed=%t", size, remaining, want, conn.closed)
		}
		if conn.deadline.Before(started) || conn.deadline.After(time.Now().Add(201*time.Millisecond)) {
			t.Fatal("missing/broad read deadline")
		}
	}
}
