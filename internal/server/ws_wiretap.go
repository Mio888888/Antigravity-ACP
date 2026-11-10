package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type WiretapEvent struct {
	Timestamp time.Time `json:"timestamp"`
	WorkerID  string    `json:"worker_id"`
	Direction string    `json:"direction"` // "IN" | "OUT"
	RawLine   string    `json:"raw_line"`
}

type wiretapClient struct {
	conn            *websocket.Conn
	sendCh          chan []byte
	telemetryOnly   bool
	lastStreamNs    int64
}

type WiretapHub struct {
	mu      sync.Mutex
	clients map[*websocket.Conn]*wiretapClient
	recent  []WiretapEvent
}

func NewWiretapHub() *WiretapHub {
	return &WiretapHub{
		clients: make(map[*websocket.Conn]*wiretapClient),
		recent:  make([]WiretapEvent, 0, 200),
	}
}

func (h *WiretapHub) Broadcast(workerID, direction, rawLine string) {
	now := time.Now().UTC()
	isControlEvent := direction == "AUDIT" || direction == "STATE"

	h.mu.Lock()
	if !isControlEvent {
		const maxRecentRawLineBytes = 64 * 1024
		recentLine := rawLine
		if len(recentLine) > maxRecentRawLineBytes {
			recentLine = recentLine[:maxRecentRawLineBytes] + "...[truncated]"
		}
		evRecent := WiretapEvent{
			Timestamp: now,
			WorkerID:  workerID,
			Direction: direction,
			RawLine:   recentLine,
		}
		if len(h.recent) >= 200 {
			copy(h.recent, h.recent[1:])
			h.recent[len(h.recent)-1] = evRecent
		} else {
			h.recent = append(h.recent, evRecent)
		}
	}

	if len(h.clients) == 0 {
		h.mu.Unlock()
		return
	}

	var rawFull []byte
	var rawLight []byte
	nowNs := now.UnixNano()
	for _, cl := range h.clients {
		var msg []byte
		if cl.telemetryOnly && !isControlEvent {
			// Throttle high-frequency IN/OUT stream notifications for telemetry clients to 200ms
			// and omit potentially multi-megabyte RawLine payloads.
			if nowNs-cl.lastStreamNs < int64(200*time.Millisecond) {
				continue
			}
			cl.lastStreamNs = nowNs
			if rawLight == nil {
				rawLight, _ = json.Marshal(WiretapEvent{
					Timestamp: now,
					WorkerID:  workerID,
					Direction: direction,
				})
			}
			msg = rawLight
		} else {
			if rawFull == nil {
				rawFull, _ = json.Marshal(WiretapEvent{
					Timestamp: now,
					WorkerID:  workerID,
					Direction: direction,
					RawLine:   rawLine,
				})
			}
			msg = rawFull
		}
		if len(msg) == 0 {
			continue
		}
		select {
		case cl.sendCh <- msg:
		default:
			// If a client's buffer is full on a critical control event, drop the oldest frame to make room
			if isControlEvent {
				select {
				case <-cl.sendCh:
				default:
				}
				select {
				case cl.sendCh <- msg:
				default:
				}
			}
		}
	}
	h.mu.Unlock()
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: isSameOriginRequest,
}

func (h *WiretapHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	telemetryOnly := r.URL.Query().Get("mode") == "telemetry"
	cl := &wiretapClient{
		conn:          conn,
		sendCh:        make(chan []byte, 128),
		telemetryOnly: telemetryOnly,
	}

	h.mu.Lock()
	if !telemetryOnly {
		for _, ev := range h.recent {
			if raw, err := json.Marshal(ev); err == nil {
				select {
				case cl.sendCh <- raw:
				default:
				}
			}
		}
	}
	h.clients[conn] = cl
	h.mu.Unlock()

	doneCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-doneCh:
				return
			case msg, ok := <-cl.sendCh:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}()

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			h.mu.Lock()
			delete(h.clients, conn)
			h.mu.Unlock()
			close(doneCh)
			_ = conn.Close()
			return
		}
	}
}
