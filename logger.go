package logrus

import (
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

type Logger struct {
	// The logs are `io.Copy`'d to this in a mutex. It's common to set this to a
	// file, or leave it default which is `os.Stderr`. You can also set this to
	// something more adventorous, such as logging to Kafka.
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
	// The logging level the logger should log at. This is typically (and defaults
	// to) `logrus.Info`, which allows Info(), Warn(), Error() and Fatal() to be
	// logged.
	Level Level
	// Used to sync writing to the log. Locking is enabled by Default
	mu MutexWrap
	// Reusable empty entry
	entryPool sync.Pool
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

// Creates a new logger. Configuration should be set by changing `Formatter`,
// `Out` and `Hooks` directly on the default logger instance. You can also just
// instantiate your own:
//
//    var log = &Logger{
//      Out: os.Stderr,
//      Formatter: new(JSONFormatter),
//      Hooks: make(LevelHooks),
//      Level: logrus.DebugLevel,
//    }
//
// It's recommended to make this a global instance called `log`.
func New() *Logger {
	var defaultFormatter *DetailedFormatter
	ldf := os.Getenv("LOGRUS_DEFAULT_FORMATTER")
	switch ldf {
	case "json":
		defaultFormatter = &DetailedFormatter{
			ChildFormatter: &JSONFormatter{},
			Package:        (os.Getenv("LOGRUS_DETAILS_INCLUDE_PACKAGE") == "true"),
			File:           (os.Getenv("LOGRUS_DETAILS_INCLUDE_FILE") == "true"),
			Function:       (os.Getenv("LOGRUS_DETAILS_INCLUDE_FUNCTION") == "true"),
			Line:           (os.Getenv("LOGRUS_DETAILS_INCLUDE_LINE") == "true"),
			BaseNameOnly:   !(os.Getenv("LOGRUS_DETAILS_INCLUDE_BASE_NAME_ONLY") == "false"),
		}
	default:
		defaultFormatter = &DetailedFormatter{
			ChildFormatter: &TextFormatter{},
			Package:        (os.Getenv("LOGRUS_DETAILS_INCLUDE_PACKAGE") == "true"),
			File:           (os.Getenv("LOGRUS_DETAILS_INCLUDE_FILE") == "true"),
			Function:       (os.Getenv("LOGRUS_DETAILS_INCLUDE_FUNCTION") == "true"),
			Line:           (os.Getenv("LOGRUS_DETAILS_INCLUDE_LINE") == "true"),
			BaseNameOnly:   !(os.Getenv("LOGRUS_DETAILS_INCLUDE_BASE_NAME_ONLY") == "false"),
		}
	}

	lll := os.Getenv("LOGRUS_LOG_LEVEL")
	if lll == "" {
		lll = os.Getenv("PORTAL_LOG_LEVEL")
	}

	logLevel := InfoLevel

	switch strings.ToLower(lll) {
	case "error":
		logLevel = ErrorLevel
	case "warn":
		logLevel = WarnLevel
	case "debug":
		logLevel = DebugLevel
	default:
		logLevel = InfoLevel
	}

	return &Logger{
		Out:       os.Stderr,
		Formatter: defaultFormatter,
		Hooks:     make(LevelHooks),
		Level:     logLevel,
	}
}

func (logger *Logger) newEntry() *Entry {
	entry, ok := logger.entryPool.Get().(*Entry)
	if ok {
		return entry
	}
	return NewEntry(logger)
}

func (logger *Logger) releaseEntry(entry *Entry) {
	logger.entryPool.Put(entry)
}

// Adds a field to the log entry, note that it doesn't log until you call
// Debug, Print, Info, Warn, Fatal or Panic. It only creates a log entry.
// If you want multiple fields, use `WithFields`.
func (logger *Logger) WithField(key string, value interface{}) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithField(key, value)
}

// Adds a struct of fields to the log entry. All it does is call `WithField` for
// each `Field`.
func (logger *Logger) WithFields(fields Fields) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithFields(fields)
}

// Add an error as single field to the log entry.  All it does is call
// `WithError` for the given `error`.
func (logger *Logger) WithError(err error) *Entry {
	entry := logger.newEntry()
	defer logger.releaseEntry(entry)
	return entry.WithError(err)
}

func (logger *Logger) Debugf(format string, args ...interface{}) {
	if logger.level() >= DebugLevel {
		entry := logger.newEntry()
		entry.Debugf(format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Infof(format string, args ...interface{}) {
	if logger.level() >= InfoLevel {
		entry := logger.newEntry()
		entry.Infof(format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Printf(format string, args ...interface{}) {
	entry := logger.newEntry()
	entry.Printf(format, args...)
	logger.releaseEntry(entry)
}

func (logger *Logger) Warnf(format string, args ...interface{}) {
	if logger.level() >= WarnLevel {
		entry := logger.newEntry()
		entry.Warnf(format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Warningf(format string, args ...interface{}) {
	if logger.level() >= WarnLevel {
		entry := logger.newEntry()
		entry.Warnf(format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Errorf(format string, args ...interface{}) {
	if logger.level() >= ErrorLevel {
		entry := logger.newEntry()
		entry.Errorf(format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Fatalf(format string, args ...interface{}) {
	if logger.level() >= FatalLevel {
		entry := logger.newEntry()
		entry.Fatalf(format, args...)
		logger.releaseEntry(entry)
	}
	Exit(1)
}

func (logger *Logger) Panicf(format string, args ...interface{}) {
	if logger.level() >= PanicLevel {
		entry := logger.newEntry()
		entry.Panicf(format, args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Debug(args ...interface{}) {
	if logger.level() >= DebugLevel {
		entry := logger.newEntry()
		entry.Debug(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Info(args ...interface{}) {
	if logger.level() >= InfoLevel {
		entry := logger.newEntry()
		entry.Info(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Print(args ...interface{}) {
	entry := logger.newEntry()
	entry.Info(args...)
	logger.releaseEntry(entry)
}

func (logger *Logger) Warn(args ...interface{}) {
	if logger.level() >= WarnLevel {
		entry := logger.newEntry()
		entry.Warn(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Warning(args ...interface{}) {
	if logger.level() >= WarnLevel {
		entry := logger.newEntry()
		entry.Warn(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Error(args ...interface{}) {
	if logger.level() >= ErrorLevel {
		entry := logger.newEntry()
		entry.Error(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Fatal(args ...interface{}) {
	if logger.level() >= FatalLevel {
		entry := logger.newEntry()
		entry.Fatal(args...)
		logger.releaseEntry(entry)
	}
	Exit(1)
}

func (logger *Logger) Panic(args ...interface{}) {
	if logger.level() >= PanicLevel {
		entry := logger.newEntry()
		entry.Panic(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Debugln(args ...interface{}) {
	if logger.level() >= DebugLevel {
		entry := logger.newEntry()
		entry.Debugln(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Infoln(args ...interface{}) {
	if logger.level() >= InfoLevel {
		entry := logger.newEntry()
		entry.Infoln(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Println(args ...interface{}) {
	entry := logger.newEntry()
	entry.Println(args...)
	logger.releaseEntry(entry)
}

func (logger *Logger) Warnln(args ...interface{}) {
	if logger.level() >= WarnLevel {
		entry := logger.newEntry()
		entry.Warnln(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Warningln(args ...interface{}) {
	if logger.level() >= WarnLevel {
		entry := logger.newEntry()
		entry.Warnln(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Errorln(args ...interface{}) {
	if logger.level() >= ErrorLevel {
		entry := logger.newEntry()
		entry.Errorln(args...)
		logger.releaseEntry(entry)
	}
}

func (logger *Logger) Fatalln(args ...interface{}) {
	if logger.level() >= FatalLevel {
		entry := logger.newEntry()
		entry.Fatalln(args...)
		logger.releaseEntry(entry)
	}
	Exit(1)
}

func (logger *Logger) Panicln(args ...interface{}) {
	if logger.level() >= PanicLevel {
		entry := logger.newEntry()
		entry.Panicln(args...)
		logger.releaseEntry(entry)
	}
}

//When file is opened with appending mode, it's safe to
//write concurrently to a file (within 4k message on Linux).
//In these cases user can choose to disable the lock.
func (logger *Logger) SetNoLock() {
	logger.mu.Disable()
}

func (logger *Logger) level() Level {
	return Level(atomic.LoadUint32((*uint32)(&logger.Level)))
}

func (logger *Logger) SetLevel(level Level) {
	atomic.StoreUint32((*uint32)(&logger.Level), uint32(level))
}
