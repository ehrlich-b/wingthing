package logger

import (
	"io"
	"log/slog"
	"os"
)

var Log *slog.Logger

// Init initializes the global logger
func Init(level string, logFile string) error {
	// Parse log level
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelDebug
	}

	// Set up multi-writer (stdout + file)
	var writers []io.Writer
	writers = append(writers, os.Stdout)

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return err
		}
		writers = append(writers, f)
	}

	multiWriter := io.MultiWriter(writers...)

	// Create handler with custom options
	handler := slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
		Level: logLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Shorten time format
			if a.Key == slog.TimeKey {
				return slog.String("time", a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	})

	Log = slog.New(handler)
	slog.SetDefault(Log)

	return nil
}

// Debug logs at debug level
func Debug(msg string, args ...any) {
	Log.Debug(msg, args...)
}

// Info logs at info level
func Info(msg string, args ...any) {
	Log.Info(msg, args...)
}

// Warn logs at warn level
func Warn(msg string, args ...any) {
	Log.Warn(msg, args...)
}

// Error logs at error level
func Error(msg string, args ...any) {
	Log.Error(msg, args...)
}
