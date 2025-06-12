package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/binding"
	"github.com/cloudevents/sdk-go/v2/client"
	cehttp "github.com/cloudevents/sdk-go/v2/protocol/http"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

//go:embed ui/build
var reactUI embed.FS

// Broadcaster handles the distribution of events to all connected SSE and WebSocket clients.
type Broadcaster struct {
	sync.RWMutex
	sseClients       map[string]chan cloudevents.Event
	websocketClients map[string]chan cloudevents.Event
	logger           *slog.Logger
}

// NewBroadcaster creates a new Broadcaster instance.
func NewBroadcaster(logger *slog.Logger) *Broadcaster {
	return &Broadcaster{
		sseClients:       make(map[string]chan cloudevents.Event),
		websocketClients: make(map[string]chan cloudevents.Event),
		logger:           logger,
	}
}

// AddSSEClient registers a new SSE client to receive events.
func (b *Broadcaster) AddSSEClient(clientID string) chan cloudevents.Event {
	b.Lock()
	defer b.Unlock()
	ch := make(chan cloudevents.Event, 100) // Increased buffer size
	b.sseClients[clientID] = ch
	b.logger.Info("SSE client added", "clientID", clientID, "totalSSSClients", len(b.sseClients))
	return ch
}

// RemoveSSEClient removes an SSE client.
func (b *Broadcaster) RemoveSSEClient(clientID string) {
	b.Lock()
	defer b.Unlock()
	if ch, ok := b.sseClients[clientID]; ok {
		close(ch)
		delete(b.sseClients, clientID)
		b.logger.Info("SSE client removed", "clientID", clientID, "totalSSSClients", len(b.sseClients))
	}
}

// AddWebSocketClient registers a new WebSocket client.
func (b *Broadcaster) AddWebSocketClient(clientID string) chan cloudevents.Event {
	b.Lock()
	defer b.Unlock()
	ch := make(chan cloudevents.Event, 100) // Increased buffer size
	b.websocketClients[clientID] = ch
	b.logger.Info("WebSocket client added", "clientID", clientID, "totalWSClients", len(b.websocketClients))
	return ch
}

// RemoveWebSocketClient removes a WebSocket client.
func (b *Broadcaster) RemoveWebSocketClient(clientID string) {
	b.Lock()
	defer b.Unlock()
	if ch, ok := b.websocketClients[clientID]; ok {
		close(ch)
		delete(b.websocketClients, clientID)
		b.logger.Info("WebSocket client removed", "clientID", clientID, "totalWSClients", len(b.websocketClients))
	}
}

// Broadcast sends an event to all registered clients.
func (b *Broadcaster) Broadcast(event cloudevents.Event) {
	b.RLock()
	defer b.RUnlock()
	b.logger.Info("Broadcasting event to clients", "eventType", event.Type(), "numSSE", len(b.sseClients), "numWS", len(b.websocketClients))

	for id, ch := range b.sseClients {
		select {
		case ch <- event:
		default:
			b.logger.Warn("SSE client channel full, dropping event", "clientID", id)
		}
	}

	for id, ch := range b.websocketClients {
		select {
		case ch <- event:
		default:
			b.logger.Warn("WebSocket client channel full, dropping event", "clientID", id)
		}
	}
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWebSocket(c *gin.Context, broadcaster *Broadcaster, ceClient client.Client, logger *slog.Logger, defaultSource string) {
	conn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error("Failed to upgrade WebSocket connection", "error", err)
		return
	}
	defer conn.Close()

	clientID := uuid.New().String()
	eventChan := broadcaster.AddWebSocketClient(clientID)
	defer broadcaster.RemoveWebSocketClient(clientID)

	go func() {
		for event := range eventChan {
			if err := conn.WriteJSON(event); err != nil {
				logger.Error("Failed to write JSON to WebSocket", "error", err, "clientID", clientID)
				return
			}
		}
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			logger.Info("WebSocket connection closed", "clientID", clientID, "reason", err)
			break
		}

		event := cloudevents.NewEvent()
		event.SetID(uuid.New().String())
		event.SetSource(defaultSource)
		event.SetType("websocket.message.v1")
		event.SetData(cloudevents.ApplicationJSON, message)

		if result := ceClient.Send(context.Background(), event); !cloudevents.IsACK(result) {
			logger.Error("Failed to send CloudEvent from WebSocket", "error", result.Error())
		}
	}
}

func handleSSE(c *gin.Context, broadcaster *Broadcaster, logger *slog.Logger) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")

	clientID := uuid.New().String()
	eventChan := broadcaster.AddSSEClient(clientID)
	defer broadcaster.RemoveSSEClient(clientID)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming unsupported"})
		return
	}

	fmt.Fprintf(c.Writer, "event: connection\ndata: Connected with clientID %s\n\n", clientID)
	flusher.Flush()

	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				return
			}
			eventJSON, err := json.Marshal(event)
			if err != nil {
				continue
			}
			fmt.Fprintf(c.Writer, "event: %s\n", event.Type())
			fmt.Fprintf(c.Writer, "id: %s\n", event.ID())
			fmt.Fprintf(c.Writer, "data: %s\n\n", string(eventJSON))
			flusher.Flush()
		case <-c.Request.Context().Done():
			return
		}
	}
}

// LogEntry defines the structure of a single log received from the hook.
type LogEntry struct {
	Level   string                 `json:"level"`
	Message string                 `json:"message"`
	Module  string                 `json:"module"`
	Data    map[string]interface{} `json:"data"`
}

func formatEventType(moduleName string) string {
	if moduleName == "" {
		return "log.entry.v1.unknown"
	}
	return "log." + strings.ReplaceAll(moduleName, "/", ".")
}

// handleLogSinkBatch processes an array of log entries.
func handleLogSinkBatch(c *gin.Context, ceClient client.Client, logger *slog.Logger, defaultSource string) {
	var logEntries []LogEntry
	if err := c.ShouldBindJSON(&logEntries); err != nil {
		logger.Error("Failed to bind batch log sink JSON", "error", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON payload for batch"})
		return
	}

	logger.Info("Received log batch", "count", len(logEntries))

	// Process each log entry in the batch sequentially to maintain order.
	for _, logEntry := range logEntries {
		eventType := formatEventType(logEntry.Module)

		event := cloudevents.NewEvent()
		event.SetID(uuid.New().String())
		event.SetSource(defaultSource)
		event.SetType(eventType)
		event.SetData(cloudevents.ApplicationJSON, logEntry)

		// Send the individual event to the Knative Broker.
		if result := ceClient.Send(context.Background(), event); !cloudevents.IsACK(result) {
			logger.Error("Failed to send CloudEvent from log batch", "error", result.Error())
		}
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "batch accepted", "count": len(logEntries)})
}

func handleLogSink(c *gin.Context, ceClient client.Client, logger *slog.Logger, defaultSource string) {
	var logEntry LogEntry
	if err := c.ShouldBindJSON(&logEntry); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON payload"})
		return
	}
	//eventType := formatEventType(logEntry.Module)
	eventType := "github.com.icgdevspace.logevent" // Ned to use a consistent event type for all log entries, so the client applications can know what to listen for.
	event := cloudevents.NewEvent()
	event.SetID(uuid.New().String())
	event.SetSource(defaultSource)
	event.SetType(eventType)
	event.SetData(cloudevents.ApplicationJSON, logEntry)
	if result := ceClient.Send(context.Background(), event); !cloudevents.IsACK(result) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to send event to broker"})
	} else {
		c.JSON(http.StatusAccepted, gin.H{"status": "event accepted", "eventId": event.ID()})
	}
}

func handleCloudEventPost(c *gin.Context, broadcaster *Broadcaster, logger *slog.Logger) {
	message := cehttp.NewMessageFromHttpRequest(c.Request)
	event, err := binding.ToEvent(c.Request.Context(), message)
	if err != nil {
		logger.Error("Failed to extract CloudEvent", "error", err)
		c.Status(http.StatusBadRequest)
		return
	}
	broadcaster.Broadcast(*event)
	c.Status(http.StatusOK)
}

func main() {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	sinkURL := os.Getenv("K_SINK")
	if sinkURL == "" {
		slog.Error("FATAL: K_SINK environment variable not set.")
		os.Exit(1)
	}
	defaultEventSource := os.Getenv("DEFAULT_EVENT_SOURCE")
	if defaultEventSource == "" {
		defaultEventSource = "urn:k8s:knative-logevent-router"
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	broadcaster := NewBroadcaster(logger)

	ceClient, err := cloudevents.NewClientHTTP(cehttp.WithTarget(sinkURL))
	if err != nil {
		slog.Error("Failed to create CloudEvents client", "error", err)
		os.Exit(1)
	}

	router := gin.Default()

	// --- Static UI Serving ---
	// Create a sub-filesystem that points to the 'build' directory inside the embed.FS
	uiFS, err := fs.Sub(reactUI, "ui/build")
	if err != nil {
		slog.Error("Failed to create sub-filesystem for UI", "error", err)
		os.Exit(1)
	}
	// Serve static files from the UI build directory
	router.StaticFS("/ui", http.FS(uiFS))
	// Redirect root to the UI
	router.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/ui")
	})

	router.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Accept")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// --- API Endpoints ---
	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/readyz", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/ws", func(c *gin.Context) { handleWebSocket(c, broadcaster, ceClient, logger, defaultEventSource) })
	router.GET("/events", func(c *gin.Context) { handleSSE(c, broadcaster, logger) })

	router.POST("/eventsink", func(c *gin.Context) { handleLogSink(c, ceClient, logger, defaultEventSource) })
	router.POST("/eventsink/batch", func(c *gin.Context) { handleLogSinkBatch(c, ceClient, logger, defaultEventSource) })

	router.POST("/", func(c *gin.Context) {
		handleCloudEventPost(c, broadcaster, logger)
	})
	router.POST("/cloudevents", func(c *gin.Context) {
		handleCloudEventPost(c, broadcaster, logger)
	})

	srv := &http.Server{Addr: ":" + port, Handler: router}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server failed", "error", err)
			os.Exit(1)
		}
	}()

	slog.Info("Server started", "port", port, "message", "Navigate to /ui to view the Event Inspector")
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("Server forced to shutdown", "error", err)
	}
	slog.Info("Server exiting")
}
