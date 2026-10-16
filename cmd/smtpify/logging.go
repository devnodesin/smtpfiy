package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// levels maps a level name to its numeric severity. Higher means more severe.
var levels = map[string]int{
	"debug": 0,
	"info":  1,
	"warn":  2,
	"error": 3,
}

// maskMarker replaces sensitive values in log output.
const maskMarker = "***"

// Logger writes leveled, formatted log entries to one or two destinations.
// It is safe for concurrent use.
type Logger struct {
	mu       sync.Mutex
	level    int
	stdio    *log.Logger
	file     *log.Logger
	fileDest io.Closer
}

// New returns a Logger writing to the given stdio writer at the supplied level
// name (debug/info/warn/error). If fileDest is non-nil, output is also written
// there (dual write). Unknown levels default to info.
func New(stdio io.Writer, fileDest io.Writer, level string) *Logger {
	var closer io.Closer
	if f, ok := fileDest.(*os.File); ok {
		closer = f
	}
	return newLogger(stdio, fileDest, closer, level)
}

// NewFromFile returns a Logger that writes to stdio and, additionally, to the
// file at logFile (dual write). If the file cannot be opened, logging still
// continues to stdio and no error is returned.
func NewFromFile(stdio io.Writer, logFile, level string) (*Logger, error) {
	var dest *os.File
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			dest = f
		}
	}
	return newLogger(stdio, dest, dest, level), nil
}

// NewFromConfig builds a Logger from a level name and optional log file path.
func NewFromConfig(stdio io.Writer, level, logFile string) (*Logger, error) {
	return NewFromFile(stdio, logFile, level)
}

func newLogger(stdio io.Writer, fileDest io.Writer, closer io.Closer, level string) *Logger {
	lvl, ok := levels[strings.ToLower(level)]
	if !ok {
		lvl = levels["info"]
	}
	l := &Logger{
		level:    lvl,
		stdio:    log.New(stdio, "", 0),
		fileDest: closer,
	}
	if fileDest != nil {
		l.file = log.New(fileDest, "", 0)
	}
	return l
}

// Close releases the file destination if one was opened. Stdio is never closed.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fileDest != nil {
		_ = l.fileDest.Close()
		l.fileDest = nil
		l.file = nil
	}
}

func (l *Logger) enabled(level int) bool {
	return level >= l.level
}

func (l *Logger) write(name, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s %-5s %s", time.Now().Format(time.RFC3339), name, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.stdio.Println(line)
	if l.file != nil {
		l.file.Println(line)
	}
}

// Debug logs at debug level.
func (l *Logger) Debug(format string, args ...interface{}) {
	if l.enabled(levels["debug"]) {
		l.write("DEBUG", format, args...)
	}
}

// Info logs at info level.
func (l *Logger) Info(format string, args ...interface{}) {
	if l.enabled(levels["info"]) {
		l.write("INFO", format, args...)
	}
}

// Warn logs at warn level.
func (l *Logger) Warn(format string, args ...interface{}) {
	if l.enabled(levels["warn"]) {
		l.write("WARN", format, args...)
	}
}

// Error logs at error level.
func (l *Logger) Error(format string, args ...interface{}) {
	if l.enabled(levels["error"]) {
		l.write("ERROR", format, args...)
	}
}

// MaskAndInfo logs at info level with sensitive arguments masked. Any argument
// equal to a non-empty sensitive value is replaced with "***" in the message.
func (l *Logger) MaskAndInfo(format string, sensitive ...string) {
	if !l.enabled(levels["info"]) {
		return
	}
	masked := make([]string, len(sensitive))
	for i, s := range sensitive {
		if s == "" {
			masked[i] = ""
		} else {
			masked[i] = maskMarker
		}
	}
	parts := make([]interface{}, len(masked))
	for i, m := range masked {
		parts[i] = m
	}
	l.write("INFO", format, parts...)
}
