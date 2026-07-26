package server

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/coder/websocket"
)

func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.render(w, r, "terminal", map[string]any{
		"Title": a.Name + " terminal",
		"App":   s.appView(r, a),
	})
}

// termMessage is the client -> server control protocol: keyboard input or a
// terminal resize. Server -> client traffic is raw binary TTY output.
type termMessage struct {
	Type string `json:"t"`              // "i" = input, "r" = resize
	Data string `json:"d,omitempty"`    // input bytes
	Cols uint   `json:"cols,omitempty"` // resize
	Rows uint   `json:"rows,omitempty"`
}

// handleTerminalWS bridges a websocket to a TTY exec inside the container.
func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusInternalError, "closed")

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	shell, execID, err := s.dock.InteractiveShell(ctx, a)
	if err != nil {
		conn.Write(ctx, websocket.MessageBinary, []byte("failed to open shell: "+err.Error()+"\r\n"))
		conn.Close(websocket.StatusNormalClosure, "no shell")
		return
	}
	defer shell.Close()
	// A root shell in the container can read every secret the app holds, so
	// opening one is worth recording even though nothing was changed.
	s.audit(r, "terminal.open", a.Name, "")

	// Container output -> websocket.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := shell.Reader.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				conn.Close(websocket.StatusNormalClosure, "shell exited")
				return
			}
		}
	}()

	// Websocket -> container input (plus resize control messages).
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg termMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "i":
			if _, err := io.WriteString(shell.Conn, msg.Data); err != nil {
				return
			}
		case "r":
			if msg.Cols > 0 && msg.Rows > 0 {
				s.dock.ResizeShell(ctx, execID, msg.Rows, msg.Cols)
			}
		default:
			log.Printf("terminal: unknown message type %q", msg.Type)
		}
	}
}
