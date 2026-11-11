package viewer

import (
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
)

const pkgPrefix = "github.com/Walon-Foundation/viewer-sdk-go."

// withException adds the OpenTelemetry exception fields for the first error among the
// fields (spec SDK-07). The stack is taken for error and fatal records only.
func withException(fields []field, level Level) []field {
	for _, f := range fields {
		if f.Value.Kind() != slog.KindAny {
			continue
		}
		err, ok := f.Value.Any().(error)
		if !ok || err == nil {
			continue
		}
		fields = append(fields,
			field{Key: "exception.type", Value: slog.StringValue(errorType(err))},
			field{Key: "exception.message", Value: slog.StringValue(err.Error())},
		)
		if level >= LevelError {
			fields = append(fields, field{Key: "exception.stacktrace", Value: slog.StringValue(callerStack())})
		}
		return fields
	}
	return fields
}

// errorType names the error's type, looking through fmt.Errorf's %w wrappers only: a
// wrapped *fs.PathError reads as *fs.PathError, not *fmt.wrapError, and not the
// syscall.Errno inside it, which says less.
func errorType(err error) string {
	for {
		t := fmt.Sprintf("%T", err)
		if t != "*fmt.wrapError" && t != "*fmt.wrapErrors" {
			return t
		}
		next := errors.Unwrap(err)
		if next == nil {
			return t // %w with several errors: no single type to name
		}
		err = next
	}
}

// callerStack is the stack from the code that logged, skipping this package and log/slog.
func callerStack() string {
	pcs := make([]uintptr, 48)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	var b strings.Builder
	leading := true
	for count := 0; count < 32; {
		fr, more := frames.Next()
		inside := strings.HasPrefix(fr.Function, pkgPrefix) || strings.HasPrefix(fr.Function, "log/slog.")
		if leading && inside && !strings.HasSuffix(fr.File, "_test.go") {
			if !more {
				break
			}
			continue
		}
		leading = false
		b.WriteString(fr.Function)
		b.WriteString("\n\t")
		b.WriteString(fr.File)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(fr.Line))
		b.WriteByte('\n')
		count++
		if !more {
			break
		}
	}
	return b.String()
}
