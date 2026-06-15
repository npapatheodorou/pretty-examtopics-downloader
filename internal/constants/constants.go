package constants

import "time"

// Request behaviour
const HttpTimeout = 20 * time.Second

// MaxConcurrentRequests caps in-flight requests. Note the adaptive limiter
// (below) gates how fast new requests *start*, so this is an upper bound on
// parallelism, not the effective throughput on its own.
const MaxConcurrentRequests = 15

// RequestsPerSecond is the fixed pace used by the sequential retry pass.
const RequestsPerSecond = 2.0
const MaxRetries = 3

// Adaptive request pacing (AIMD). The main fan-out starts at
// StartRequestsPerSecond and only speeds up by RateIncreaseStep after
// SuccessStreakForSpeedup consecutive successful responses, never exceeding
// MaxRequestsPerSecond. Any throttling signal (HTTP 429/503) halves the rate
// down toward MinRequestsPerSecond. Defaults are deliberately conservative so
// behaviour matches the old fixed 2 rps until the server proves tolerant.
const StartRequestsPerSecond = 2.0
const MinRequestsPerSecond = 0.5
const MaxRequestsPerSecond = 8.0
const RateIncreaseStep = 0.5
const SuccessStreakForSpeedup = 8

// Backoff configuration
const InitalBackoff = time.Second
const BackoffFactor = 2.0

// HTTP Transport Tuning (in http client)
const MaxIdleConns = 100
const MaxIdleConnsPerHost = 100
const MaxConnsPerHost = 100

// Connection Timeouts (also in http client)
const IdleConnTimeout = 90 * time.Second
const TLSHandshakeTimeout = 10 * time.Second
const ResponseHeaderTimeout = 10 * time.Second
const ExpectContinueTimeout = 1 * time.Second
