package safaribidi

import "time"

// EngineKind is the capability kind reported for this engine.
const EngineKind = "safari-bidi"

// Options configures the safari-bidi engine.
type Options struct {
	// DriverPath overrides the safaridriver location. Empty uses DriverPath.
	DriverPath string
	// Diagnose passes --diagnose so safaridriver writes a session log to
	// ~/Library/Logs/com.apple.WebDriver.
	Diagnose bool
	// DriverReadyTimeout bounds the wait for safaridriver's HTTP server.
	DriverReadyTimeout time.Duration
	// SessionTimeout bounds session creation. Safari's own failure path takes
	// 30s, so anything shorter reports a timeout of ours instead of Apple's
	// diagnosable one.
	SessionTimeout time.Duration
	// RequestTimeout bounds a single BiDi command.
	RequestTimeout time.Duration
}

func (o Options) driverReadyTimeout() time.Duration {
	if o.DriverReadyTimeout > 0 {
		return o.DriverReadyTimeout
	}
	return 10 * time.Second
}

func (o Options) sessionTimeout() time.Duration {
	if o.SessionTimeout > 0 {
		return o.SessionTimeout
	}
	return 60 * time.Second
}

func (o Options) requestTimeout() time.Duration {
	if o.RequestTimeout > 0 {
		return o.RequestTimeout
	}
	return 30 * time.Second
}
