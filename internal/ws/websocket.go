// Package ws provides WebSocket server for real-time flow updates.
package ws

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/HakAl/langley/internal/config"
	"github.com/HakAl/langley/internal/store"
)

// sessionCookieName must match the cookie name used in api package.
const sessionCookieName = "langley_session"

// isLocalhostOrigin checks if the Origin header indicates a localhost request.
func isLocalhostOrigin(origin string) bool {
	return strings.HasPrefix(origin, "http://localhost") ||
		strings.HasPrefix(origin, "http://127.0.0.1") ||
		strings.HasPrefix(origin, "https://localhost") ||
		strings.HasPrefix(origin, "https://127.0.0.1")
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Only allow localhost origins
		origin := r.Header.Get("Origin")
		return origin == "" || isLocalhostOrigin(origin)
	},
}

// Hub manages WebSocket connections and message broadcasting.
type Hub struct {
	cfg       *config.Config
	logger    *slog.Logger
	clients   map[*Client]bool
	broadcast chan *Message
	register  chan *Client
	unregister chan *Client
	mu        sync.RWMutex
}

// Client represents a WebSocket client connection.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// Message types for WebSocket communication.
const (
	MessageTypeFlowStart   = "flow_start"
	MessageTypeFlowUpdate  = "flow_update"
	MessageTypeFlowComplete = "flow_complete"
	MessageTypeEvent       = "event"
	MessageTypePing        = "ping"
)

// Message is a WebSocket message.
type Message struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// NewHub creates a new WebSocket hub.
func NewHub(cfg *config.Config, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}

	return &Hub{
		cfg:        cfg,
		logger:     logger,
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *Message, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub's main loop.
func (h *Hub) Run(ctx context.Context) {
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Close all clients
			h.mu.Lock()
			for client := range h.clients {
				close(client.send)
				delete(h.clients, client)
			}
			h.mu.Unlock()
			return

		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			h.logger.Debug("client connected", "clients", len(h.clients))

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			h.logger.Debug("client disconnected", "clients", len(h.clients))

		case message := <-h.broadcast:
			data, err := json.Marshal(message)
			if err != nil {
				h.logger.Error("failed to marshal message", "error", err)
				continue
			}

			// Collect clients to remove under read lock (no mutation)
			h.mu.RLock()
			var toRemove []*Client
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					// Client buffer full, mark for removal
					toRemove = append(toRemove, client)
				}
			}
			h.mu.RUnlock()

			// Remove slow clients under write lock
			if len(toRemove) > 0 {
				h.mu.Lock()
				for _, client := range toRemove {
					// Double-check membership to avoid double-close if unregister ran concurrently
					if _, ok := h.clients[client]; ok {
						delete(h.clients, client)
						close(client.send)
					}
				}
				h.mu.Unlock()
			}

		case <-pingTicker.C:
			h.Broadcast(&Message{
				Type:      MessageTypePing,
				Timestamp: time.Now(),
			})
		}
	}
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msg *Message) {
	select {
	case h.broadcast <- msg:
	default:
		h.logger.Warn("broadcast channel full, dropping message")
	}
}

// BroadcastFlowStart broadcasts a flow start event.
func (h *Hub) BroadcastFlowStart(flow *store.Flow) {
	h.Broadcast(&Message{
		Type:      MessageTypeFlowStart,
		Timestamp: time.Now(),
		Data:      flowToSummary(flow),
	})
}

// BroadcastFlowUpdate broadcasts a flow update event.
func (h *Hub) BroadcastFlowUpdate(flow *store.Flow) {
	h.Broadcast(&Message{
		Type:      MessageTypeFlowUpdate,
		Timestamp: time.Now(),
		Data:      flowToSummary(flow),
	})
}

// BroadcastFlowComplete broadcasts a flow completion event.
func (h *Hub) BroadcastFlowComplete(flow *store.Flow) {
	h.Broadcast(&Message{
		Type:      MessageTypeFlowComplete,
		Timestamp: time.Now(),
		Data:      flowToSummary(flow),
	})
}

// BroadcastEvent broadcasts an SSE event.
func (h *Hub) BroadcastEvent(event *store.Event) {
	h.Broadcast(&Message{
		Type:      MessageTypeEvent,
		Timestamp: time.Now(),
		Data:      event,
	})
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Handler returns an HTTP handler for WebSocket connections.
// Uses constant-time comparison to prevent timing attacks.
// NOTE: Token is read from h.cfg.Auth.Token to support hot-reload.
//
// Authentication modes (checked in order):
// 1. Session cookie - browser sends automatically
// 2. Authorization header - for CLI
// 3. Token query param - for CLI (WebSocket can't set headers easily)
func (h *Hub) Handler(authToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Read current token from config (supports hot-reload)
		currentToken := authToken
		if h.cfg != nil {
			currentToken = h.cfg.Auth.Token
		}

		authenticated := false

		// 1. Check session cookie first
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(currentToken)) == 1 {
			authenticated = true
		}

		// 2. Check Authorization header
		if !authenticated {
			auth := r.Header.Get("Authorization")
			expectedAuth := "Bearer " + currentToken
			if subtle.ConstantTimeCompare([]byte(auth), []byte(expectedAuth)) == 1 {
				authenticated = true
			}
		}

		// 3. Check token query param (for CLI tools that can't set headers)
		if !authenticated {
			token := r.URL.Query().Get("token")
			if subtle.ConstantTimeCompare([]byte(token), []byte(currentToken)) == 1 {
				authenticated = true
			}
		}

		// Validate Origin if present (security check)
		origin := r.Header.Get("Origin")
		if origin != "" && !isLocalhostOrigin(origin) {
			h.logger.Warn("rejected non-localhost WebSocket origin", "origin", origin)
			http.Error(w, "Forbidden: non-localhost origin", http.StatusForbidden)
			return
		}

		if !authenticated {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			h.logger.Error("failed to upgrade connection", "error", err)
			return
		}

		client := &Client{
			hub:  h,
			conn: conn,
			send: make(chan []byte, 256),
		}

		h.register <- client

		// Start client goroutines
		go client.writePump()
		go client.readPump()
	}
}

// writePump pumps messages from the hub to the websocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, _ = w.Write(message)

			// Batch any queued messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				_, _ = w.Write([]byte{'\n'})
				_, _ = w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump pumps messages from the websocket connection to the hub.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(512)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.logger.Debug("websocket error", "error", err)
			}
			break
		}
	}
}

// flowToSummary converts a flow to a summary for WebSocket broadcast.
func flowToSummary(f *store.Flow) map[string]interface{} {
	summary := map[string]interface{}{
		"id":        f.ID,
		"host":      f.Host,
		"method":    f.Method,
		"path":      f.Path,
		"is_sse":    f.IsSSE,
		"timestamp": f.Timestamp,
		"provider":  f.Provider,
	}

	if f.StatusCode != nil {
		summary["status_code"] = *f.StatusCode
	}
	if f.DurationMs != nil {
		summary["duration_ms"] = *f.DurationMs
	}
	if f.TaskID != nil {
		summary["task_id"] = *f.TaskID
	}
	if f.TaskSource != nil {
		summary["task_source"] = *f.TaskSource
	}
	if f.Model != nil {
		summary["model"] = *f.Model
	}
	if f.InputTokens != nil {
		summary["input_tokens"] = *f.InputTokens
	}
	if f.OutputTokens != nil {
		summary["output_tokens"] = *f.OutputTokens
	}
	if f.TotalCost != nil {
		summary["total_cost"] = *f.TotalCost
	}

	return summary
}
