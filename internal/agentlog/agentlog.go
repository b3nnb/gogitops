// Package agentlog provides activity logging for the gogitops agent.
// Logs are written to ~/.cache/gogitops/agent.log as JSON lines.
package agentlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogEntry is a single activity log record
type LogEntry struct {
	Timestamp string `json:"ts"`
	Level     string `json:"level"` // info, warn, error, action
	Category  string `json:"cat"`   // git, service, peer, alert, agent, disk
	Message   string `json:"msg"`
	Detail    string `json:"detail,omitempty"`
}

// Logger writes structured logs to a file
type Logger struct {
	mu   sync.Mutex
	file *os.File
}

var (
	defaultLogger *Logger
	once          sync.Once
)

// Default returns the singleton logger
func Default() *Logger {
	once.Do(func() {
		defaultLogger = MustNew()
	})
	return defaultLogger
}

// MustNew creates a new logger, panicking if the file can't be opened
func MustNew() *Logger {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	logDir := filepath.Join(cacheDir, "gogitops")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "agent.log")

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Fallback to stderr
		return &Logger{file: os.Stderr}
	}
	return &Logger{file: f}
}

// Log writes an entry
func (l *Logger) Log(level, category, msg, detail string) {
	entry := LogEntry{
		Timestamp: time.Now().Format("2006-01-02T15:04:05"),
		Level:     level,
		Category:  category,
		Message:   msg,
		Detail:    detail,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	data, _ := json.Marshal(entry)
	l.file.Write(data)
	l.file.Write([]byte("\n"))
}

// Convenience methods
func (l *Logger) Info(cat, msg string) {
	l.Log("info", cat, msg, "")
}
func (l *Logger) Infof(cat, format string, args ...interface{}) {
	l.Log("info", cat, fmt.Sprintf(format, args...), "")
}
func (l *Logger) Warn(cat, msg string) {
	l.Log("warn", cat, msg, "")
}
func (l *Logger) Warnf(cat, format string, args ...interface{}) {
	l.Log("warn", cat, fmt.Sprintf(format, args...), "")
}
func (l *Logger) Error(cat, msg string) {
	l.Log("error", cat, msg, "")
}
func (l *Logger) Errorf(cat, format string, args ...interface{}) {
	l.Log("error", cat, fmt.Sprintf(format, args...), "")
}
func (l *Logger) Action(cat, msg string) {
	l.Log("action", cat, msg, "")
}
func (l *Logger) Actionf(cat, format string, args ...interface{}) {
	l.Log("action", cat, fmt.Sprintf(format, args...), "")
}

// ReadEntries reads the last N log entries from the file
func ReadEntries(n int) ([]LogEntry, error) {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	logPath := filepath.Join(cacheDir, "gogitops", "agent.log")

	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}

	lines := splitLines(string(data))
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	var entries []LogEntry
	for _, line := range lines {
		if line == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// LogPath returns the path to the log file
func LogPath() string {
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache")
	}
	return filepath.Join(cacheDir, "gogitops", "agent.log")
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
