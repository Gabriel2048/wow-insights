package main

import (
	"io"
	"log/slog"
)

// newLogger returns the shipped binary's logger: JSON lines on stdout,
// spelled the way Cloud Logging reads them. slog's names are level and msg;
// Cloud Logging grades on severity and message, and without the rename every
// line lands as DEFAULT severity and nothing can be alerted on. The trace id
// the request middleware carries as trace_id becomes the field Cloud Logging
// groups a request's lines by, which needs the project name to be spelt out.
func newLogger(w io.Writer, project string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.LevelKey:
				// The key is slog's own, but any handler attribute keyed
				// "level" arrives here too, and one that is not a Level
				// must pass through rather than take the process down.
				if l, ok := a.Value.Any().(slog.Level); ok {
					return slog.String("severity", severity(l))
				}
				return a
			case slog.MessageKey:
				return slog.String("message", a.Value.String())
			case "trace_id":
				if project == "" {
					return a
				}
				return slog.String("logging.googleapis.com/trace", "projects/"+project+"/traces/"+a.Value.String())
			}
			return a
		},
	}))
}

// severity maps slog's levels onto Cloud Logging's names. They agree except
// for WARN, which Cloud Logging calls WARNING.
func severity(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARNING"
	case l >= slog.LevelInfo:
		return "INFO"
	}
	return "DEBUG"
}
