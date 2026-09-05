package config

import (
	"log/slog"
	"os"
	"strings"
)

// secretKeys are log attribute names that must never carry a value.
//
// Redaction here is a backstop, not the control: the design keeps credentials
// out of anything that reaches a logger in the first place. This exists because
// a backstop costs almost nothing and the failure it prevents is severe.
var secretKeys = []string{
	"password", "token", "secret", "api_key", "apikey", "apitoken",
	"authorization", "cookie", "credential", "config",
}

// NewLogger builds the process logger.
func NewLogger(env Environment) *slog.Logger {
	level := slog.LevelInfo
	if !env.IsProduction() {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redact}

	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if !env.IsProduction() {
		// Human-readable locally; structured JSON where something will parse it.
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	log := slog.New(handler)

	// The package default becomes this one too.
	//
	// Redaction that only applies to loggers passed down by hand is redaction
	// with a hole in it: the failure paths at the top of each binary reach for
	// slog.Error directly, and those are precisely the lines that carry a
	// database URL or a key service address inside a connection error. Setting
	// the default means there is no unredacted handler left in the process to
	// reach for by accident.
	slog.SetDefault(log)
	return log
}

// redact replaces the value of any attribute whose name suggests a secret.
func redact(_ []string, a slog.Attr) slog.Attr {
	name := strings.ToLower(a.Key)
	for _, key := range secretKeys {
		if strings.Contains(name, key) {
			return slog.String(a.Key, "[redacted]")
		}
	}
	return a
}
