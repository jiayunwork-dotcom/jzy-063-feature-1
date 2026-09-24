package push

import (
	"time"

	"github.com/gorilla/websocket"
)

// writePump drains queued frames onto the websocket until the peer closes.
func (c *Conn) writePump() {
	ticker := time.NewTicker(25 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.socket.Close()
	}()
	for {
		select {
		case frame, ok := <-c.notify:
			if !ok {
				_ = c.socket.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.socket.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.socket.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// readPump consumes client frames (mostly pongs/acks) and marks the
// connection done when the peer disconnects.
func (c *Conn) readPump(h *Hub) {
	defer func() {
		close(c.done)
		h.Unregister(c)
	}()
	c.socket.SetReadLimit(1 << 16)
	_ = c.socket.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.socket.SetPongHandler(func(string) error {
		_ = c.socket.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		if _, _, err := c.socket.ReadMessage(); err != nil {
			return
		}
		_ = c.socket.SetReadDeadline(time.Now().Add(60 * time.Second))
	}
}
