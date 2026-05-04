// Copyright 2026 The wgturn-core Authors.
// SPDX-License-Identifier: Apache-2.0

package wgturn

import (
	"fmt"
	"log"
)

// Logger is a minimal level-based logger interface. The library calls
// these methods directly; no fields, no structured arguments — keep it
// trivial so any host logger (stdlib log, slog, zap, JNI bridge, NSLog)
// can adapt with a 3-line wrapper.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// NoopLogger discards all log messages. Default when Config.Logger is nil.
type NoopLogger struct{}

// Debugf satisfies the Logger interface.
func (NoopLogger) Debugf(string, ...any) {}

// Infof satisfies the Logger interface.
func (NoopLogger) Infof(string, ...any) {}

// Warnf satisfies the Logger interface.
func (NoopLogger) Warnf(string, ...any) {}

// Errorf satisfies the Logger interface.
func (NoopLogger) Errorf(string, ...any) {}

// StdLogger forwards every level to the standard library log package
// with a level-prefixed format. Useful for CLIs and during development.
type StdLogger struct {
	// MinLevel suppresses messages below the given level. Zero means
	// "log everything" (DEBUG and above).
	MinLevel Level
}

// Level represents a log severity. Numerically ordered DEBUG < INFO < WARN < ERROR.
type Level int

// Log levels.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return fmt.Sprintf("LVL%d", int(l))
	}
}

func (s StdLogger) emit(lvl Level, format string, args ...any) {
	if lvl < s.MinLevel {
		return
	}
	log.Printf("[%s] %s", lvl, fmt.Sprintf(format, args...))
}

// Debugf satisfies the Logger interface.
func (s StdLogger) Debugf(format string, args ...any) { s.emit(LevelDebug, format, args...) }

// Infof satisfies the Logger interface.
func (s StdLogger) Infof(format string, args ...any) { s.emit(LevelInfo, format, args...) }

// Warnf satisfies the Logger interface.
func (s StdLogger) Warnf(format string, args ...any) { s.emit(LevelWarn, format, args...) }

// Errorf satisfies the Logger interface.
func (s StdLogger) Errorf(format string, args ...any) { s.emit(LevelError, format, args...) }
