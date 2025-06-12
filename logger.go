package logrus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LogFunction For big messages, it can be more efficient to pass a function
// and only call it if the log level is actually enables rather than
// generating the log message and then checking if the level is enabled
type LogFunction func() []interface{}

type Logger struct {
	// The logs are `io.Copy`'d to this in a mutex. It's common to set this to a
	// file, or leave it default which is `os.Stderr`. You can also set this to
	// something more adventurous, such as logging to Kafka.
	Out io.Writer
	// Hooks for the logger instance. These allow firing events based on logging
	// levels and log entries. For example, to send errors to an error tracking
	// service, log to StatsD or dump the core on fatal errors.
	Hooks LevelHooks
	// All log entries pass through the formatter before logged to Out. The
	// included formatters are `TextFormatter` and `JSONFormatter` for which
	// TextFormatter is the default. In development (when a TTY is attached) it
	// logs with colors, but to a file it wouldn't. You can easily implement your
	// own that implements the `Formatter` interface, see the `README` or included
	// formatters for examples.
	Formatter Formatter

	// Flag for whether to log caller info (off by default)
	ReportCaller bool

	// The logging level the logger should log at. This is typically (and defaults
	// to) `Info`, which allows Info(), Warn(), Error() and Fatal() to be
	// logged.
	Level Level
	// Used to sync writing to the log. Locking is enabled by Default
	mu MutexWrap
	// Reusable empty entry
	entryPool sync.Pool
	// Function to exit the application, defaults to `os.Exit()`
	ExitFunc exitFunc
	// The buffer pool used to format the log. If it is nil, the default global
	// buffer pool will be used.
	BufferPool BufferPool
}

type exitFunc func(int)

const (
	ColorRed    = "\033[31m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorBlue   = "\033[34m"
	ColorPurple = "\033[35m"
	ColorCyan   = "\033[36m"
	ColorReset  = "\033[0m"
)

// ICGCustomFormatter is a custom formatter for
// It formats logs into a simple, human-readable key=value format.
type ICGCustomFormatter struct {
	// TimestampFormat sets the format used for timestamps.
	TimestampFormat string
}

// Format implements the Formatter interface.
// It takes a Entry and returns the formatted log message as bytes.
func (f *ICGCustomFormatter) Format(entry *Entry) ([]byte, error) {
	var b *bytes.Buffer

	// Get a buffer from the entry's buffer pool if available.
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}

	// Use the custom timestamp format if set, otherwise default to a standard one.
	timestampFormat := f.TimestampFormat
	if timestampFormat == "" {
		timestampFormat = "2006-01-02 15:04:05.000"
	}

	// 1. Start with the timestamp.
	b.WriteString(entry.Time.Format(timestampFormat))
	b.WriteString(" | ")

	// 2. Add the colored and padded log level.
	levelColor := ""
	switch entry.Level {
	case ErrorLevel, FatalLevel, PanicLevel:
		levelColor = ColorRed
	case WarnLevel:
		levelColor = ColorYellow
	case InfoLevel:
		levelColor = ColorBlue
	case DebugLevel:
		levelColor = ColorCyan
	case TraceLevel:
		levelColor = ColorPurple
	default:
		levelColor = ColorReset
	}
	levelText := fmt.Sprintf("%-7s", strings.ToUpper(entry.Level.String()))
	fmt.Fprintf(b, "%s%s%s", levelColor, levelText, ColorReset)
	b.WriteString(" | ")

	// 3. Add the main log message.
	b.WriteString(entry.Message)

	// 4. If caller reporting is enabled, add the file and line number.
	if entry.HasCaller() {
		b.WriteString(" | ") // Separator for caller info
		fileParts := strings.Split(entry.Caller.File, "icggroup")
		fmt.Fprintf(b, "file=%s:%d", fileParts[len(fileParts)-1], entry.Caller.Line)
	}

	// 5. Finally, add a newline to terminate the log entry.
	b.WriteByte('\n')

	return b.Bytes(), nil
}

type MutexWrap struct {
	lock     sync.Mutex
	disabled bool
}

func (mw *MutexWrap) Lock() {
	if !mw.disabled {
		mw.lock.Lock()
	}
}

func (mw *MutexWrap) Unlock() {
	if !mw.disabled {
		mw.lock.Unlock()
	}
}

func (mw *MutexWrap) Disable() {
	mw.disabled = true
}

// New creates a new logger.
// This function is intended for initializing the standard logger and for uses
// where graceful hook shutdown is not required. For applications that need
// to ensure logs are sent before exiting, use `NewWithHook()` instead.
func New() *Logger {
	logger, _ := NewWithHook()
	return logger
}

// NewWithHook creates a new Logger and initializes the KnativeHook if configured.
// It returns both the logger and the hook, allowing the calling application
// to manage the hook's lifecycle (e.g., calling `hook.Shutdown()`).
func NewWithHook() (*Logger, Hook) {
	logger := &Logger{
		Out:          os.Stderr,
		Formatter:    new(ICGCustomFormatter),
		Hooks:        make(LevelHooks),
		Level:        InfoLevel,
		ExitFunc:     os.Exit,
		ReportCaller: true,
		BufferPool: &defaultPool{
			pool: &sync.Pool{
				New: func() interface{} {
					return new(bytes.Buffer)
				},
			},
		},
	}

	logger.entryPool = sync.Pool{
		New: func() interface{} {
			return NewEntry(logger)
		},
	}

	logger.mu = MutexWrap{
		lock:     sync.Mutex{},
		disabled: false,
	}

	var knativeHook Hook

	sinkURL := os.Getenv("LOGRUS_KNATIVE_SINK_URL")
	if sinkURL == "" {
		// No warning is printed here because the standard logger will call this.
		// A warning would appear on every startup even if the hook isn't used.
	} else {
		hook, err := NewKnativeHook(
			sinkURL,
			WithMinLevel(InfoLevel),
			WithAsync(true),
			WithBufferSize(10000),
			WithNumWorkers(1),
			WithBatchSize(200),
			WithBatchTimeout(2*time.Second),
			WithDLQPath(os.Getenv("LOGRUS_HOOK_DLQ_PATH")),
		)
		if err != nil {
			logger.Fatalf("Failed to create Knative hook: %v", err)
		}
		logger.AddHook(hook)
		knativeHook = hook // Store the hook to return it.
	}

	return logger, knativeHook
}

func (logger *Logger) newEntry() *Entry {
	entry, ok := logger.entryPool.Get().(*Entry)
	if ok {
		return entry
	}
	return NewEntry(logger)
}

func (logger *Logger) releaseEntry(entry *Entry) {
	entry.Data = map[string]interface{}{}
	logger.entryPool.Put(entry)
}

// WithField allocates a new entry and adds a field to it.
func (logger *Logger) WithField(key string, value interface{}) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithField(key, value)
}

// WithFields adds a struct of fields to the log entry.
func (logger *Logger) WithFields(fields Fields) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithFields(fields)
}

// WithError adds an error as single field to the log entry.
func (logger *Logger) WithError(err error) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithError(err)
}

// WithContext adds a context to the log entry.
func (logger *Logger) WithContext(ctx context.Context) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithContext(ctx)
}

// WithPayload adds a payload to the log entry.
func (logger *Logger) WithPayload(payload interface{}) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	// If the payload is not empty, we add it as a field named "payload".
	if payload != nil {
		return entry.WithField("payload", payload)
	}
	return entry
}

// WithTime overrides the time of the log entry.
func (logger *Logger) WithTime(t time.Time) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithTime(t)
}

func (logger *Logger) Logf(level Level, format string, args ...interface{}) {
	if logger.IsLevelEnabled(level) {
		entry := logger.newEntry()
		entry.Logf(level, format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Tracef(format string, args ...interface{}) {
	logger.Logf(TraceLevel, format, args...)
}

func (logger *Logger) Debugf(format string, args ...interface{}) {
	logger.Logf(DebugLevel, format, args...)
}

func (logger *Logger) Infof(format string, args ...interface{}) {
	logger.Logf(InfoLevel, format, args...)
}

func (logger *Logger) Printf(format string, args ...interface{}) {
	entry := logger.newEntry()
	entry.Printf(format, args...)
	logger.releaseEntry(entry)
}

func (logger *Logger) Warnf(format string, args ...interface{}) {
	logger.Logf(WarnLevel, format, args...)
}

func (logger *Logger) Warningf(format string, args ...interface{}) {
	logger.Warnf(format, args...)
}

func (logger *Logger) Errorf(format string, args ...interface{}) {
	logger.Logf(ErrorLevel, format, args...)
}

func (logger *Logger) Fatalf(format string, args ...interface{}) {
	logger.Logf(FatalLevel, format, args...)
	logger.Exit(1)
}

func (logger *Logger) Panicf(format string, args ...interface{}) {
	logger.Logf(PanicLevel, format, args...)
}

// Log will log a message at the level given as parameter.
func (logger *Logger) Log(level Level, args ...interface{}) {
	if logger.IsLevelEnabled(level) {
		entry := logger.newEntry()
		entry.Log(level, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) LogFn(level Level, fn LogFunction) {
	if logger.IsLevelEnabled(level) {
		entry := logger.newEntry()
		entry.Log(level, fn()...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Trace(args ...interface{}) {
	logger.Log(TraceLevel, args...)
}

func (logger *Logger) Debug(args ...interface{}) {
	logger.Log(DebugLevel, args...)
}

func (logger *Logger) Info(args ...interface{}) {
	logger.Log(InfoLevel, args...)
}

func (logger *Logger) Print(args ...interface{}) {
	entry := logger.newEntry()
	entry.Print(args...)
	logger.releaseEntry(entry)
}

func (logger *Logger) Warn(args ...interface{}) {
	logger.Log(WarnLevel, args...)
}

func (logger *Logger) Warning(args ...interface{}) {
	logger.Warn(args...)
}

func (logger *Logger) Error(args ...interface{}) {
	logger.Log(ErrorLevel, args...)
}

func (logger *Logger) Fatal(args ...interface{}) {
	logger.Log(FatalLevel, args...)
	logger.Exit(1)
}

func (logger *Logger) Panic(args ...interface{}) {
	logger.Log(PanicLevel, args...)
}

func (logger *Logger) TraceFn(fn LogFunction) {
	logger.LogFn(TraceLevel, fn)
}

func (logger *Logger) DebugFn(fn LogFunction) {
	logger.LogFn(DebugLevel, fn)
}

func (logger *Logger) InfoFn(fn LogFunction) {
	logger.LogFn(InfoLevel, fn)
}

func (logger *Logger) PrintFn(fn LogFunction) {
	entry := logger.newEntry()
	entry.Print(fn()...)
	logger.releaseEntry(entry)
}

func (logger *Logger) WarnFn(fn LogFunction) {
	logger.LogFn(WarnLevel, fn)
}

func (logger *Logger) WarningFn(fn LogFunction) {
	logger.WarnFn(fn)
}

func (logger *Logger) ErrorFn(fn LogFunction) {
	logger.LogFn(ErrorLevel, fn)
}

func (logger *Logger) FatalFn(fn LogFunction) {
	logger.LogFn(FatalLevel, fn)
	logger.Exit(1)
}

func (logger *Logger) PanicFn(fn LogFunction) {
	logger.LogFn(PanicLevel, fn)
}

func (logger *Logger) Logln(level Level, args ...interface{}) {
	if logger.IsLevelEnabled(level) {
		entry := logger.newEntry()
		entry.Logln(level, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Traceln(args ...interface{}) {
	logger.Logln(TraceLevel, args...)
}

func (logger *Logger) Debugln(args ...interface{}) {
	logger.Logln(DebugLevel, args...)
}

func (logger *Logger) Infoln(args ...interface{}) {
	logger.Logln(InfoLevel, args...)
}

func (logger *Logger) Println(args ...interface{}) {
	entry := logger.newEntry()
	entry.Println(args...)
	logger.releaseEntry(entry)
}

func (logger *Logger) Warnln(args ...interface{}) {
	logger.Logln(WarnLevel, args...)
}

func (logger *Logger) Warningln(args ...interface{}) {
	logger.Warnln(args...)
}

func (logger *Logger) Errorln(args ...interface{}) {
	logger.Logln(ErrorLevel, args...)
}

func (logger *Logger) Fatalln(args ...interface{}) {
	logger.Logln(FatalLevel, args...)
	logger.Exit(1)
}

func (logger *Logger) Panicln(args ...interface{}) {
	logger.Logln(PanicLevel, args...)
}

func (logger *Logger) Exit(code int) {
	runHandlers()
	if logger.ExitFunc == nil {
		logger.ExitFunc = os.Exit
	}
	logger.ExitFunc(code)
}

func (logger *Logger) SetNoLock() {
	logger.mu.Disable()
}

func (logger *Logger) level() Level {
	return Level(atomic.LoadUint32((*uint32)(&logger.Level)))
}

// SetLevel sets the logger level.
func (logger *Logger) SetLevel(level Level) {
	atomic.StoreUint32((*uint32)(&logger.Level), uint32(level))
}

// GetLevel returns the logger level.
func (logger *Logger) GetLevel() Level {
	return logger.level()
}

// AddHook adds a hook to the logger hooks.
func (logger *Logger) AddHook(hook Hook) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.Hooks.Add(hook)
}

// IsLevelEnabled checks if the log level of the logger is greater than the level param
func (logger *Logger) IsLevelEnabled(level Level) bool {
	return logger.level() >= level
}

// SetFormatter sets the logger formatter.
func (logger *Logger) SetFormatter(formatter Formatter) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.Formatter = formatter
}

// SetOutput sets the logger output.
func (logger *Logger) SetOutput(output io.Writer) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.Out = output
}

func (logger *Logger) SetReportCaller(reportCaller bool) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.ReportCaller = reportCaller
}

// ReplaceHooks replaces the logger hooks and returns the old ones
func (logger *Logger) ReplaceHooks(hooks LevelHooks) LevelHooks {
	logger.mu.Lock()
	oldHooks := logger.Hooks
	logger.Hooks = hooks
	logger.mu.Unlock()
	return oldHooks
}

// SetBufferPool sets the logger buffer pool.
func (logger *Logger) SetBufferPool(pool BufferPool) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	logger.BufferPool = pool
}
