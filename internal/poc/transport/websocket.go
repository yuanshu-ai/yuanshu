package transport

import (
	"context"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/yuanshu-ai/yuanshu/internal/poc/protocol"
)

type websocketEndpoint struct {
	conn       *websocket.Conn
	readLimit  int
	writeLimit int
	once       sync.Once
}

func WebSocketEndpoint(conn *websocket.Conn, readLimit, writeLimit int) Endpoint {
	conn.SetReadLimit(int64(readLimit))
	return &websocketEndpoint{conn: conn, readLimit: readLimit, writeLimit: writeLimit}
}

func DialWebSocket(ctx context.Context, url string, header http.Header, readLimit, writeLimit int) (Endpoint, error) {
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	return WebSocketEndpoint(conn, readLimit, writeLimit), nil
}

func (w *websocketEndpoint) Send(ctx context.Context, f protocol.Frame) error {
	b, err := protocol.Encode(f, w.writeLimit)
	if err != nil {
		return err
	}
	if err := w.conn.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	return nil
}
func (w *websocketEndpoint) Receive(ctx context.Context) (protocol.Frame, error) {
	typ, b, err := w.conn.Read(ctx)
	if err != nil {
		return protocol.Frame{}, err
	}
	if typ != websocket.MessageText {
		return protocol.Frame{}, protocol.ErrInvalidFrame
	}
	return protocol.Decode(b, w.readLimit)
}
func (w *websocketEndpoint) Close() error {
	var err error
	w.once.Do(func() { err = w.conn.Close(websocket.StatusNormalClosure, "closed") })
	return err
}
