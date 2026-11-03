package node

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// maxWSMessage bounds one WebSocket message. yamux writes a 12-byte header
// and a body of at most one stream window (256 KiB) per message.
const maxWSMessage = 1 << 20

var errTextMessage = errors.New("loomplane-node: a text message on the connection")

// wsConn carries a byte stream over WebSocket binary messages, so yamux can
// run on it: each Write is one message and Read joins them. It is the node's
// half of the hub's wrapper. It also remembers the close code the control
// plane hung up with, which yamux would otherwise swallow.
type wsConn struct {
	c         *websocket.Conn
	rmu       sync.Mutex
	r         io.Reader
	wmu       sync.Mutex
	closeCode atomic.Int32
}

func newWSConn(c *websocket.Conn) *wsConn {
	c.SetReadLimit(maxWSMessage)
	return &wsConn{c: c}
}

// code is the WebSocket close code received, or 0.
func (w *wsConn) code() int { return int(w.closeCode.Load()) }

func (w *wsConn) Read(p []byte) (int, error) {
	w.rmu.Lock()
	defer w.rmu.Unlock()
	for {
		if w.r == nil {
			typ, r, err := w.c.NextReader()
			if err != nil {
				var ce *websocket.CloseError
				if errors.As(err, &ce) {
					w.closeCode.Store(int32(ce.Code)) //nolint:gosec // close codes are 16-bit
				}
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return 0, io.EOF
				}
				return 0, err
			}
			if typ != websocket.BinaryMessage {
				return 0, errTextMessage
			}
			w.r = r
		}
		n, err := w.r.Read(p)
		if errors.Is(err, io.EOF) {
			w.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (w *wsConn) Write(p []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	if err := w.c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsConn) Close() error                       { return w.c.Close() }
func (w *wsConn) LocalAddr() net.Addr                { return w.c.LocalAddr() }
func (w *wsConn) RemoteAddr() net.Addr               { return w.c.RemoteAddr() }
func (w *wsConn) SetReadDeadline(t time.Time) error  { return w.c.SetReadDeadline(t) }
func (w *wsConn) SetWriteDeadline(t time.Time) error { return w.c.SetWriteDeadline(t) }
func (w *wsConn) SetDeadline(t time.Time) error {
	return errors.Join(w.c.SetReadDeadline(t), w.c.SetWriteDeadline(t))
}
