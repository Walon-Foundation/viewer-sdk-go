package viewer

import (
	"fmt"
	"strings"
)

// Level is a record's severity, numbered as OpenTelemetry severity numbers.
type Level int

// The six levels (spec SDK-01).
const (
	LevelTrace Level = 1
	LevelDebug Level = 5
	LevelInfo  Level = 9
	LevelWarn  Level = 13
	LevelError Level = 17
	LevelFatal Level = 21
)

// String is the level's lower-case name: "info", "error".
func (l Level) String() string {
	switch {
	case l >= LevelFatal:
		return "fatal"
	case l >= LevelError:
		return "error"
	case l >= LevelWarn:
		return "warn"
	case l >= LevelInfo:
		return "info"
	case l >= LevelDebug:
		return "debug"
	case l >= LevelTrace:
		return "trace"
	}
	return fmt.Sprintf("level(%d)", int(l))
}

// ParseLevel reads a level name, in any case. "warning" is accepted for warn.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return LevelDebug, nil
	case "info":
		return LevelInfo, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "error":
		return LevelError, nil
	case "fatal":
		return LevelFatal, nil
	}
	return 0, fmt.Errorf("viewer: unknown level %q", s)
}
