package logger

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Level represents log severity.
type Level int

const (
	DebugLevel Level = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warn"
	case ErrorLevel:
		return "error"
	default:
		return "unknown"
	}
}

// Logger provides structured JSON logging.
type Logger struct {
	level  Level
	output io.Writer
}

// New creates a Logger with the specified level.
func New(level Level, output io.Writer) *Logger {
	if output == nil {
		output = os.Stdout
	}
	return &Logger{level: level, output: output}
}

// logEntry is the internal JSON structure.
type logEntry struct {
	Timestamp string         `json:"timestamp"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

func (l *Logger) log(level Level, msg string, fields map[string]any) {
	if level < l.level {
		return
	}
	entry := logEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level.String(),
		Message:   msg,
		Fields:    fields,
	}
	data, _ := json.Marshal(entry)
	fmt.Fprintln(l.output, string(data))
}

func (l *Logger) Debug(msg string, fields ...map[string]any) { l.log(DebugLevel, msg, merge(fields...)) }
func (l *Logger) Info(msg string, fields ...map[string]any)  { l.log(InfoLevel, msg, merge(fields...)) }
func (l *Logger) Warn(msg string, fields ...map[string]any)  { l.log(WarnLevel, msg, merge(fields...)) }
func (l *Logger) Error(msg string, fields ...map[string]any) { l.log(ErrorLevel, msg, merge(fields...)) }

func merge(fields ...map[string]any) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	if len(fields) == 1 {
		return fields[0]
	}
	out := make(map[string]any)
	for _, f := range fields {
		for k, v := range f {
			out[k] = v
		}
	}
	return out
}