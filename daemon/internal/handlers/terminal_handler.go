package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// TerminalHandler serves an authenticated PTY over WebSocket at /ws/terminal.
//
// Protocol (all messages are JSON):
//
//	Client → Server:
//	  {"type":"input",  "data":"<raw keystrokes>"}
//	  {"type":"resize", "cols":120, "rows":40}
//	  {"type":"ping"}
//
//	Server → Client:
//	  {"type":"output", "data":"<raw terminal output>"}
//	  {"type":"error",  "data":"<message>"}
//	  {"type":"exit"}
//
// Auth: session is validated by the global sessionMiddleware before this
// handler is reached. No additional auth step required here.
type TerminalHandler struct{}

func NewTerminalHandler() *TerminalHandler {
	return &TerminalHandler{}
}

type termMsg struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

func (h *TerminalHandler) HandleTerminal(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("terminal: websocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Determine shell - prefer bash, fall back to sh
	shell, err := exec.LookPath("bash")
	if err != nil {
		shell, err = exec.LookPath("sh")
		if err != nil {
			log.Printf("terminal: no shell found: %v", err)
			sendTermMsg(conn, "error", "No shell (bash/sh) found on system")
			return
		}
	}

	cmd := exec.Command(shell, "--login")
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("terminal: pty start error: %v", err)
		sendTermMsg(conn, "error", "Failed to start terminal: "+err.Error())
		return
	}
	defer func() {
		ptmx.Close()
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()          //nolint:errcheck
	}()

	// PTY → WebSocket (output)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				msg, _ := json.Marshal(termMsg{Type: "output", Data: string(buf[:n])})
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}
			if err != nil {
				// PTY closed - shell exited
				sendTermMsg(conn, "exit", "")
				return
			}
		}
	}()

	// WebSocket → PTY (input / resize)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg termMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			ptmx.Write([]byte(msg.Data)) //nolint:errcheck
		case "resize":
			cols, rows := msg.Cols, msg.Rows
			if cols == 0 {
				cols = 80
			}
			if rows == 0 {
				rows = 24
			}
			pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows}) //nolint:errcheck
		case "ping":
			// no-op keepalive
		}
	}
}

func sendTermMsg(conn *websocket.Conn, msgType, data string) {
	msg, _ := json.Marshal(termMsg{Type: msgType, Data: data})
	conn.WriteMessage(websocket.TextMessage, msg) //nolint:errcheck
}

