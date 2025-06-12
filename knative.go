package logrus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4" // For retry logic
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultEventSource  = "logrus-knative-hook"
	defaultEventType    = "com.example.logevent"
	defaultMinLevel     = DebugLevel // Default minimum level to send logs
	defaultBatchSize    = 100
	defaultBatchTimeout = 5 * time.Second
	defaultBufferSize   = 1000
	// FIX: numWorkers is now a constant 1 to guarantee order.
	numWorkers              = 1
	defaultHTTPTimeout      = 30 * time.Second
	defaultRetryMaxInterval = 1 * time.Minute
)

// logPayload defines the JSON structure sent to the eventbus sink,
// making it compatible with the handleLogSink endpoint.
type logPayload struct {
	Level    string                 `json:"level"`
	Message  string                 `json:"message"`
	Module   string                 `json:"module"`
	Data     map[string]interface{} `json:"data"`
	CallSign string                 `json:"callsign,omitempty"` // Optional, can be used for additional identification
}

// Option defines a functional option for configuring the KnativeHook.
type Option func(*KnativeHook)

// KnativeHook sends log entries to a Knative sink as CloudEvents.
type KnativeHook struct {
	knativeSinkURL   string
	tracer           trace.Tracer
	eventSource      string // Retained for potential future use or context
	defaultEventType string // Retained for potential future use or context
	minLevel         Level
	httpClient       *http.Client // Underlying HTTP client for sending logs

	callSign       string // Optional callsign for additional identification
	forceAllEvents bool   // If true, all events are sent regardless of whether a payload is present

	// Asynchronous processing
	async          bool
	eventQueue     chan entryWithContext
	wg             sync.WaitGroup
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// Configuration for asynchronous processing
	batchSize    int
	batchTimeout time.Duration
	bufferSize   int

	// DLQ (Dead Letter Queue)
	dlqPath string
	dlqFile *os.File
	dlqLock sync.Mutex
}

// entryWithContext wraps a logrus entry with its OpenTelemetry context.
type entryWithContext struct {
	entry   *Entry
	otelCtx context.Context
}

// WithEventSource sets the CloudEvent source attribute.
func WithEventSource(source string) Option {
	return func(h *KnativeHook) {
		if source != "" {
			h.eventSource = source
		}
	}
}

// WithDefaultEventType sets the default CloudEvent type attribute.
func WithDefaultEventType(eventType string) Option {
	return func(h *KnativeHook) {
		if eventType != "" {
			h.defaultEventType = eventType
		}
	}
}

// WithMinLevel sets the minimum log level for the hook to fire.
func WithMinLevel(level Level) Option {
	return func(h *KnativeHook) {
		h.minLevel = level
	}
}

// WithHTTPClient allows providing a custom http.Client.
func WithHTTPClient(client *http.Client) Option {
	return func(h *KnativeHook) {
		h.httpClient = client
	}
}

// WithAsync enables asynchronous event sending.
func WithAsync(enabled bool) Option {
	return func(h *KnativeHook) {
		h.async = enabled
	}
}

func WithNumWorkers(num int) Option {
	return func(h *KnativeHook) {
		if num > 0 {
			h.wg.Add(1)
			h.async = true
			h.eventQueue = make(chan entryWithContext, h.bufferSize)
			h.shutdownCtx, h.shutdownCancel = context.WithCancel(context.Background())
			go h.worker() // Start the worker to process events
			fmt.Fprintf(os.Stderr, "KnativeHook: Using %d worker(s) for ordered processing\n", num)
		} else {
			fmt.Fprintf(os.Stderr, "KnativeHook: Invalid number of workers %d, using default single worker\n", num)
			h.wg.Add(1)
			h.async = true
			h.eventQueue = make(chan entryWithContext, h.bufferSize)
			h.shutdownCtx, h.shutdownCancel = context.WithCancel(context.Background())
		}
	}
}

// WithBatchSize sets the batch size for async sending.
func WithBatchSize(size int) Option {
	return func(h *KnativeHook) {
		if size > 0 {
			h.batchSize = size
		}
	}
}

// WithBatchTimeout sets the batch timeout for async sending.
func WithBatchTimeout(timeout time.Duration) Option {
	return func(h *KnativeHook) {
		if timeout > 0 {
			h.batchTimeout = timeout
		}
	}
}

// WithBufferSize sets the buffer size for the async event queue.
func WithBufferSize(size int) Option {
	return func(h *KnativeHook) {
		if size > 0 {
			h.bufferSize = size
		}
	}
}

// WithDLQPath sets the path for the Dead Letter Queue file.
func WithDLQPath(path string) Option {
	return func(h *KnativeHook) {
		h.dlqPath = path
	}
}

// NewKnativeHook creates a new Logrus hook for sending events to Knative.
func NewKnativeHook(sinkURL string, opts ...Option) (*KnativeHook, error) {
	if sinkURL == "" {
		return nil, fmt.Errorf("Knative sink URL cannot be empty")
	}

	forceAllEvents := false
	fa := os.Getenv("LOGRUS_KNATIVE_FORCE_ALL_EVENTS")
	if fa != "" {
		if fa == "true" || fa == "1" {
			forceAllEvents = true
		}
	}

	hook := &KnativeHook{
		knativeSinkURL:   sinkURL,
		eventSource:      defaultEventSource,
		defaultEventType: defaultEventType,
		minLevel:         defaultMinLevel,
		batchSize:        defaultBatchSize,
		batchTimeout:     defaultBatchTimeout,
		bufferSize:       defaultBufferSize,
		forceAllEvents:   forceAllEvents,
		callSign:         os.Getenv("LOGRUS_KNATIVE_CALLSIGN"),
	}

	for _, opt := range opts {
		opt(hook)
	}

	hook.tracer = otel.Tracer("github.com/icggroup/logrus")

	// Initialize a default HTTP client if one wasn't provided.
	if hook.httpClient == nil {
		hook.httpClient = &http.Client{
			Timeout: defaultHTTPTimeout,
		}
	}

	if hook.async {
		hook.eventQueue = make(chan entryWithContext, hook.bufferSize)
		hook.shutdownCtx, hook.shutdownCancel = context.WithCancel(context.Background())
		// FIX: Always start exactly one worker to guarantee ordered processing.
		hook.wg.Add(1)
		go hook.worker()
	}

	if hook.dlqPath != "" {
		dir := filepath.Dir(hook.dlqPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create DLQ directory %s: %w", dir, err)
		}
		f, err := os.OpenFile(hook.dlqPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("failed to open DLQ file %s: %w", hook.dlqPath, err)
		}
		hook.dlqFile = f
	}

	return hook, nil
}

// Levels returns the log levels for which this hook should be fired.
func (h *KnativeHook) Levels() []Level {
	levels := make([]Level, 0, len(AllLevels))
	for _, level := range AllLevels {
		if level <= h.minLevel {
			levels = append(levels, level)
		}
	}
	return levels
}

// Fire is called when a log entry is made.
func (h *KnativeHook) Fire(entry *Entry) error {

	if entry.HasCaller() {
		entry.Data["file"] = fmt.Sprintf("%s:%d", entry.Caller.File, entry.Caller.Line)
		entry.Data["function"] = entry.Caller.Function
		entry.Data["module"] = parseModule(entry.Caller.Function)
	}

	otelCtx := entry.Context
	if otelCtx == nil {
		otelCtx = context.Background()
	}

	entryCopy := entry.Dup()

	if h.async {
		select {
		case h.eventQueue <- entryWithContext{entry: entryCopy, otelCtx: otelCtx}:
		default:
			fmt.Fprintf(os.Stderr, "KnativeHook: event queue full, dropping log: %s\n", entry.Message)
		}
	} else {
		go h.sendEvent(otelCtx, entryCopy)
	}
	return nil
}

// sendEvent creates a JSON payload and POSTs it to the configured sink URL.
func (h *KnativeHook) sendEvent(ctx context.Context, entry *Entry) {
	if entry.Data != nil {
		// Determine if the data contains a payload.
		if _, ok := entry.Data["payload"]; !ok {
			// If no payload is present, skip sending this entry unless forceAllEvents is true.
			if !h.forceAllEvents {
				return
			}
		}
	}

	module, _ := entry.Data["module"].(string)
	if h.callSign != "" {
		entry.Data["callSign"] = h.callSign
	}

	payload := logPayload{
		Level:   entry.Level.String(),
		Message: entry.Message,
		Module:  module,
		Data:    entry.Data,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "KnativeHook: failed to marshal log payload: %v", err)
		h.logToDLQ(entry.Data, fmt.Errorf("failed to marshal log payload: %w", err))
		return
	}

	ctx, span := h.tracer.Start(ctx, "knativehook.sendEvent", trace.WithAttributes(
		attribute.String("eventbus.sink.url", h.knativeSinkURL),
		attribute.String("log.level", entry.Level.String()),
	))
	defer span.End()

	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = defaultRetryMaxInterval
	bo.MaxElapsedTime = 2 * time.Minute
	bo.Reset()

	sendOp := func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.knativeSinkURL, bytes.NewBuffer(jsonPayload))
		if err != nil {
			return backoff.Permanent(fmt.Errorf("failed to create HTTP request: %w", err))
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "logrus-knative-hook/1.0")
		// Add a request ID for better tracing, if desired
		req.Header.Set("X-Request-ID", uuid.NewString())

		resp, err := h.httpClient.Do(req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "HTTP request failed, retrying")
			return fmt.Errorf("HTTP request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := fmt.Errorf("received non-2xx status code: %d", resp.StatusCode)
			span.RecordError(err)
			span.SetStatus(codes.Error, "send failed with non-2xx status")
			if resp.StatusCode >= 500 {
				return err // Retry on 5xx server errors
			}
			return backoff.Permanent(err) // Do not retry on 4xx client errors
		}

		span.SetStatus(codes.Ok, "log sent successfully to eventbus")
		return nil
	}

	if err := backoff.Retry(sendOp, bo); err != nil {
		h.logToDLQ(entry.Data, fmt.Errorf("failed to send log after retries: %w", err))
	}
}

// worker processes events from the queue.
func (h *KnativeHook) worker() {
	defer h.wg.Done()
	batch := make([]entryWithContext, 0, h.batchSize)
	timer := time.NewTimer(h.batchTimeout)
	defer timer.Stop()

	for {
		select {
		case <-h.shutdownCtx.Done():
			if len(batch) > 0 {
				h.processBatch(batch)
			}
			return
		case ewCtx, ok := <-h.eventQueue:
			if !ok {
				if len(batch) > 0 {
					h.processBatch(batch)
				}
				return
			}
			batch = append(batch, ewCtx)
			if len(batch) >= h.batchSize {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				h.processBatch(batch)
				batch = make([]entryWithContext, 0, h.batchSize)
				timer.Reset(h.batchTimeout)
			}
		case <-timer.C:
			if len(batch) > 0 {
				h.processBatch(batch)
				batch = make([]entryWithContext, 0, h.batchSize)
			}
			timer.Reset(h.batchTimeout)
		}
	}
}

// processBatch sends a batch of events sequentially to maintain order.
func (h *KnativeHook) processBatch(batch []entryWithContext) {
	// FIX: The batch is now processed in a simple sequential loop
	// instead of concurrently, guaranteeing order.
	for _, ewCtx := range batch {
		h.sendEvent(ewCtx.otelCtx, ewCtx.entry)
	}
}

// Shutdown gracefully stops the hook, attempting to send any pending log entries.
func (h *KnativeHook) Shutdown(timeout time.Duration) {
	if !h.async {
		if h.dlqFile != nil {
			h.dlqFile.Close()
		}
		return
	}

	h.shutdownCancel()
	close(h.eventQueue)

	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		if h.dlqFile != nil {
			h.dlqFile.Close()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintf(os.Stderr, "KnativeHook: shutdown timed out after %v, some logs might have been lost\n", timeout)
	}
}

// logToDLQ writes a failed event payload to the dead-letter queue file.
func (h *KnativeHook) logToDLQ(payload map[string]interface{}, sendErr error) {
	if h.dlqFile == nil {
		fmt.Fprintf(os.Stderr, "KnativeHook: DLQ not configured. Failed to send event: %v. Payload: %v\n", sendErr, payload)
		return
	}

	dlqEntry := map[string]interface{}{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"error":     sendErr.Error(),
		"payload":   payload,
	}

	dlqBytes, jsonErr := json.Marshal(dlqEntry)
	if jsonErr != nil {
		fmt.Fprintf(os.Stderr, "KnativeHook: Failed to marshal DLQ entry: %v. Original error: %v. Payload: %v\n", jsonErr, sendErr, payload)
		return
	}

	h.dlqLock.Lock()
	defer h.dlqLock.Unlock()

	if _, writeErr := h.dlqFile.Write(append(dlqBytes, '\n')); writeErr != nil {
		fmt.Fprintf(os.Stderr, "KnativeHook: Failed to write to DLQ file %s: %v. Original error: %v. Payload: %v\n", h.dlqPath, writeErr, sendErr, payload)
	}
}

// parseModule extracts the package path from a fully qualified function name.
func parseModule(fullFuncName string) string {
	pc := make([]uintptr, 10)
	n := runtime.Callers(2, pc)
	if n > 0 {
		frames := runtime.CallersFrames(pc[:n])
		for {
			frame, more := frames.Next()
			if strings.Contains(frame.Function, fullFuncName) || strings.HasSuffix(frame.Function, fullFuncName) {
				lastSlash := strings.LastIndex(frame.Function, "/")
				if lastSlash > 0 {
					dot := strings.Index(frame.Function[lastSlash:], ".")
					if dot > 0 {
						return frame.Function[:lastSlash+dot]
					}
				}
				return frame.Function
			}
			if !more {
				break
			}
		}
	}

	if fullFuncName == "" {
		return "unknown_module"
	}
	lastDot := strings.LastIndex(fullFuncName, ".")
	if lastDot == -1 {
		return fullFuncName
	}
	paren := strings.LastIndex(fullFuncName[:lastDot], ")")
	if paren != -1 {
		return fullFuncName[:strings.LastIndex(fullFuncName[:paren], ".")]
	}
	return fullFuncName[:lastDot]
}
