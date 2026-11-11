package viewer

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ConsoleMode says how records are written to the console (spec SDK-06).
type ConsoleMode int

const (
	// ConsoleAuto writes readable text, or JSON lines when Env is "production" or "prod".
	ConsoleAuto ConsoleMode = iota
	ConsoleText
	ConsoleJSON
	ConsoleOff
)

// Sink receives batches of records in place of the HTTP transport. Viewer itself uses one
// to write its own logs straight to its database; tests use one to see what was sent.
type Sink interface {
	Export(ctx context.Context, records []Record) error
}

// Config sets up a Logger. Every field is optional; empty ones fall back to the
// environment and then to the defaults below.
type Config struct {
	// Endpoint is the OTLP logs URL, e.g. https://viewer.example.org/v1/logs. A base URL
	// gets /v1/logs appended. Env: VIEWER_ENDPOINT.
	Endpoint string
	// APIKey is the project's key. Env: VIEWER_API_KEY.
	APIKey string
	// Service names the program. Env: VIEWER_SERVICE. Default: the executable's name.
	Service string
	// Env is the deployment environment. Env: VIEWER_ENV. Default: "development".
	Env string
	// Level is the lowest level recorded. Env: VIEWER_LEVEL. Default: LevelInfo.
	Level Level
	// Console picks the console format; ConsoleOff turns it off.
	Console ConsoleMode
	// Output is where console lines go. Default: os.Stdout.
	Output io.Writer
	// QueueSize, BatchSize and FlushInterval tune sending (spec SDK-03).
	// Defaults: 10 000 records, 500 records, one second.
	QueueSize     int
	BatchSize     int
	FlushInterval time.Duration
	// Resource adds attributes describing the program, sent with every batch.
	Resource map[string]any
	// Sink replaces the HTTP transport.
	Sink Sink
}

// settings is a Config with every default filled in.
type settings struct {
	endpoint, apiKey, service, env string
	level                          Level
	console                        ConsoleMode
	output                         io.Writer
	queueSize, batchSize           int
	flushInterval                  time.Duration
	resource                       map[string]any
	sink                           Sink
	// problems are reported once on stderr; none of them stop the logger.
	problems []string
}

func resolveConfig(c Config, getenv func(string) string) settings {
	s := settings{
		endpoint:      first(c.Endpoint, getenv("VIEWER_ENDPOINT")),
		apiKey:        first(c.APIKey, getenv("VIEWER_API_KEY")),
		service:       first(c.Service, getenv("VIEWER_SERVICE"), executableName()),
		env:           first(c.Env, getenv("VIEWER_ENV"), "development"),
		level:         c.Level,
		console:       c.Console,
		output:        c.Output,
		queueSize:     positive(c.QueueSize, 10_000),
		batchSize:     positive(c.BatchSize, 500),
		flushInterval: c.FlushInterval,
		resource:      c.Resource,
		sink:          c.Sink,
	}
	if s.level == 0 {
		s.level = LevelInfo
		if v := getenv("VIEWER_LEVEL"); v != "" {
			if l, err := ParseLevel(v); err == nil {
				s.level = l
			} else {
				s.problems = append(s.problems, fmt.Sprintf("VIEWER_LEVEL %q is not a level; using info", v))
			}
		}
	}
	if s.flushInterval <= 0 {
		s.flushInterval = time.Second
	}
	if s.output == nil {
		s.output = os.Stdout
	}
	if s.console == ConsoleAuto {
		s.console = ConsoleText
		if e := strings.ToLower(s.env); e == "production" || e == "prod" {
			s.console = ConsoleJSON
		}
	}
	if s.endpoint != "" {
		u, err := normalizeEndpoint(s.endpoint)
		if err != nil {
			s.problems = append(s.problems, err.Error())
			s.endpoint = ""
		} else {
			s.endpoint = u
		}
	}
	// Half a configuration is a mistake worth saying; none at all is local development.
	if s.sink == nil {
		switch {
		case s.endpoint != "" && s.apiKey == "":
			s.problems = append(s.problems, "an endpoint is set but no API key (VIEWER_API_KEY)")
		case s.endpoint == "" && s.apiKey != "":
			s.problems = append(s.problems, "an API key is set but no endpoint (VIEWER_ENDPOINT)")
		}
	}
	return s
}

// remote reports whether records leave the process.
func (s settings) remote() bool { return s.sink != nil || (s.endpoint != "" && s.apiKey != "") }

// normalizeEndpoint accepts the full logs URL or the server's base URL.
func normalizeEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("endpoint %q is not an http(s) URL", raw)
	}
	if !strings.HasSuffix(u.Path, "/v1/logs") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/v1/logs"
	}
	return u.String(), nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func positive(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func executableName() string {
	if len(os.Args) == 0 {
		return "unknown_service"
	}
	return strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
}
