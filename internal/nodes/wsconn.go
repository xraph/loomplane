package nodes

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// maxWSMessage bounds one WebSocket message. yamux writes a 12-byte header
// and a body of at most one stream window (256 KiB) per message.
const maxWSMessage = 1 << 20

// wsConn carries a byte stream over WebSocket binary messages, so yamux can
// run on it. Each Write is one message; Read joins them.
type wsConn struct {
	c   *websocket.Conn
	rmu sync.Mutex
	r   io.Reader
	wmu sync.Mutex
}

// NewWSConn wraps a WebSocket as a net.Conn of binary messages. A text
// message is an error. Both sides of a node connection use it.
func NewWSConn(c *websocket.Conn) net.Conn {
	c.SetReadLimit(maxWSMessage)
	return &wsConn{c: c}
}

var errTextMessage = errors.New("nodes: a text message on a node connection")

func (w *wsConn) Read(p []byte) (int, error) {
	w.rmu.Lock()
	defer w.rmu.Unlock()
	for {
		if w.r == nil {
			typ, r, err := w.c.NextReader()
			if err != nil {
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
