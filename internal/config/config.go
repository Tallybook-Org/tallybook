// Package config loads and validates Tallybook's environment configuration.
//
// Every value the services depend on is read here, once, at startup, and
// validated before any service starts running. A missing or malformed
// variable is a fatal startup error naming the offending variable — a
// service must never run with a zero-valued field standing in for
// configuration that was never actually supplied.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names, exactly as documented in the system prompt.
const (
	envDatabaseURL                   = "TB_DATABASE_URL"
	envStellarRPCURL                 = "TB_STELLAR_RPC_URL"
	envNetworkPassphrase             = "TB_NETWORK_PASSPHRASE"
	envPriceBookID                   = "TB_PRICE_BOOK_ID"
	envStatementRegistryID           = "TB_STATEMENT_REGISTRY_ID"
	envOperatorAddress               = "TB_OPERATOR_ADDRESS"
	envOperatorSecretSource          = "TB_OPERATOR_SECRET_SOURCE"
	envOperatorSecret                = "TB_OPERATOR_SECRET"
	envOperatorSecretPath            = "TB_OPERATOR_SECRET_PATH"
	envSafetyMarginLedgers           = "TB_SAFETY_MARGIN_LEDGERS"
	envMaxExposure                   = "TB_MAX_EXPOSURE"
	envMaxExposureAge                = "TB_MAX_EXPOSURE_AGE"
	envPeriodDuration                = "TB_PERIOD_DURATION"
	envSettlerTick                   = "TB_SETTLER_TICK"
	envIndexerStartLedger            = "TB_INDEXER_START_LEDGER"
	envCollectorAddr                 = "TB_COLLECTOR_ADDR"
	envLogLevel                      = "TB_LOG_LEVEL"
	envHTTPReadHeaderTimeout         = "TB_HTTP_READ_HEADER_TIMEOUT"
	envHTTPReadTimeout               = "TB_HTTP_READ_TIMEOUT"
	envHTTPWriteTimeout              = "TB_HTTP_WRITE_TIMEOUT"
	envHTTPIdleTimeout               = "TB_HTTP_IDLE_TIMEOUT"
	envIndexerMaxConsecutiveFailures = "TB_INDEXER_MAX_CONSECUTIVE_FAILURES"
	envSettlerMetricsAddr            = "TB_SETTLER_METRICS_ADDR"
	envIndexerAddr                   = "TB_INDEXER_ADDR"
	envIndexerTick                   = "TB_INDEXER_TICK"
	envAnchorToken                   = "TB_ANCHOR_TOKEN"
	envCollectorPeriodTick           = "TB_COLLECTOR_PERIOD_TICK"
	envCollectorAnchorTick           = "TB_COLLECTOR_ANCHOR_TICK"
)

// Defaults for the variables that are optional rather than required: the
// settler's metrics listen address, the indexer's health listen address,
// the indexer's poll interval, and the collector's two background ticks.
// They are exported so cmd/settler and cmd/indexer — and their own smoke
// tests — name the same values the loader falls back to, instead of each
// keeping its own copy of the number. The values are the ones the
// services shipped with before these variables existed, so an existing
// deployment that sets nothing behaves exactly as it did.
const (
	DefaultSettlerMetricsAddr = ":9101"
	DefaultIndexerAddr        = ":9102"
	DefaultIndexerTick        = 5 * time.Second
	// DefaultCollectorPeriodTick is how often the collector looks for
	// billing periods that have run their course. Closing a period is a
	// database read and write, not a chain call, so it can be frequent.
	DefaultCollectorPeriodTick = time.Minute
	// DefaultCollectorAnchorTick is how often the collector anchors the
	// periods that are closed but not yet on chain. Anchoring submits a
	// transaction, which costs a fee, and a closed period has no
	// deadline — nothing about a statement becomes less true by being
	// anchored later — so this is deliberately slower than the period
	// closer's tick.
	DefaultCollectorAnchorTick = 5 * time.Minute
)

const (
	defaultHTTPReadHeaderTimeout         = 5 * time.Second
	defaultHTTPReadTimeout               = 2 * time.Minute
	defaultHTTPWriteTimeout              = 5 * time.Minute
	defaultHTTPIdleTimeout               = 2 * time.Minute
	defaultIndexerMaxConsecutiveFailures = 3
)

// defaultSafetyMarginLedgers is applied when TB_SAFETY_MARGIN_LEDGERS is
// unset. Roughly two hours at current ~5s ledger close times — see §6 of
// the system prompt, which calls out that arithmetic as something to verify
// against live network conditions rather than trust indefinitely.
const defaultSafetyMarginLedgers = 1440

// SecretSource names where TB_OPERATOR_SECRET is read from.
const (
	SecretSourceEnv  = "env"
	SecretSourceFile = "file"
)

// Config holds Tallybook's full validated environment. Construct it with
// Load or LoadEnv; there is no exported way to build one with a missing or
// unvalidated field.
type Config struct {
	DatabaseURL         string
	StellarRPCURL       string
	NetworkPassphrase   string
	PriceBookID         string
	StatementRegistryID string
	OperatorAddress     string

	// OperatorSecretSource is "env" or "file". OperatorSecret holds the
	// resolved key material either way; OperatorSecretPath holds the raw
	// TB_OPERATOR_SECRET_PATH value and is only set when the source is
	// "file". See Load's doc comment for the validation rules tying these
	// together.
	OperatorSecretSource string
	OperatorSecret       Secret
	OperatorSecretPath   Secret

	SafetyMarginLedgers uint32
	MaxExposure         *big.Int
	MaxExposureAge      time.Duration
	// PeriodDuration is how long a billing period may stay open before
	// the collector closes it on calendar grounds alone, independent of
	// any price version change (§6: a period also closes "whenever the
	// price book version changes, not only at month end" — implying a
	// second, calendar-based trigger exists too; this is it).
	PeriodDuration                time.Duration
	SettlerTick                   time.Duration
	IndexerStartLedger            uint32
	CollectorAddr                 string
	LogLevel                      slog.Level
	HTTPReadHeaderTimeout         time.Duration
	HTTPReadTimeout               time.Duration
	HTTPWriteTimeout              time.Duration
	HTTPIdleTimeout               time.Duration
	IndexerMaxConsecutiveFailures uint32

	// SettlerMetricsAddr is where the settler serves /metrics, /healthz,
	// and /readyz; IndexerAddr is where the indexer serves /metrics,
	// /healthz, and /readyz; IndexerTick is how often the indexer polls
	// getEvents. All three are optional, falling back to the Default*
	// constants above — they were fixed in the binaries before they were
	// configurable, and making them adjustable is what lets two instances
	// of the same service run on one host, or an operator retune a poll
	// cadence, without editing and rebuilding.
	SettlerMetricsAddr string
	IndexerAddr        string
	IndexerTick        time.Duration

	// AnchorToken is the SEP-41 token address the operator's x402 and
	// mpp_charge statements are denominated in. It is optional: an
	// mpp_session statement takes its token from the payment channel
	// itself, and nothing in this codebase records which token an x402
	// request was paid in, so without this variable those periods are
	// metered and closed but not anchored — never anchored in a token
	// nobody chose.
	AnchorToken string

	// CollectorPeriodTick is how often the collector closes the billing
	// periods that have run their course; CollectorAnchorTick is how
	// often it anchors the ones that are closed but not yet on chain.
	// Both are optional, falling back to the Default* constants above.
	CollectorPeriodTick time.Duration
	CollectorAnchorTick time.Duration
}

// String renders the config for logs and error messages with the operator
// secret redacted. Every other field is operational metadata, not a
// credential, and is safe to print.
func (c *Config) String() string {
	if c == nil {
		return "<nil config>"
	}
	return fmt.Sprintf(
		"Config{DatabaseURL:%s StellarRPCURL:%s NetworkPassphrase:%s PriceBookID:%s "+
			"StatementRegistryID:%s OperatorAddress:%s OperatorSecretSource:%s OperatorSecret:%s "+
			"OperatorSecretPath:%s SafetyMarginLedgers:%d MaxExposure:%s MaxExposureAge:%s PeriodDuration:%s SettlerTick:%s "+
			"IndexerStartLedger:%d CollectorAddr:%s LogLevel:%s HTTPReadHeaderTimeout:%s HTTPReadTimeout:%s HTTPWriteTimeout:%s HTTPIdleTimeout:%s IndexerMaxConsecutiveFailures:%d "+
			"SettlerMetricsAddr:%s IndexerAddr:%s IndexerTick:%s AnchorToken:%s CollectorPeriodTick:%s CollectorAnchorTick:%s}",
		c.DatabaseURL, c.StellarRPCURL, c.NetworkPassphrase, c.PriceBookID,
		c.StatementRegistryID, c.OperatorAddress, c.OperatorSecretSource, c.OperatorSecret,
		c.OperatorSecretPath, c.SafetyMarginLedgers, c.MaxExposure, c.MaxExposureAge, c.PeriodDuration, c.SettlerTick,
		c.IndexerStartLedger, c.CollectorAddr, c.LogLevel, c.HTTPReadHeaderTimeout, c.HTTPReadTimeout, c.HTTPWriteTimeout, c.HTTPIdleTimeout,
		c.IndexerMaxConsecutiveFailures,
		c.SettlerMetricsAddr, c.IndexerAddr, c.IndexerTick, c.AnchorToken, c.CollectorPeriodTick, c.CollectorAnchorTick,
	)
}

// LogValue implements slog.LogValuer so passing a *Config to a logger never
// leaks the operator secret through structured output either.
func (c *Config) LogValue() slog.Value {
	return slog.StringValue(c.String())
}

// Lookup retrieves an environment variable's raw value, reporting whether it
// was set at all (distinct from being set to ""). os.LookupEnv satisfies
// this; tests supply a map-backed implementation instead of mutating the
// process environment.
type Lookup func(key string) (string, bool)

// LoadEnv loads and validates configuration from the process environment.
func LoadEnv() (*Config, error) {
	return Load(os.LookupEnv)
}

// Load loads and validates configuration using lookup as the source of
// environment variables. It fails fast on the first problem it finds,
// naming the offending variable, rather than returning a Config with any
// zero-valued field standing in for configuration that was never supplied.
//
// TB_OPERATOR_SECRET_SOURCE selects which of two variables supplies the
// operator's signing key, and Load rejects the other one being set at all
// — a stale variable left over from switching modes must fail startup, not
// be silently ignored:
//   - source "env": TB_OPERATOR_SECRET holds the key directly.
//     TB_OPERATOR_SECRET_PATH must be unset.
//   - source "file": TB_OPERATOR_SECRET_PATH holds the filesystem path to
//     a file containing the key (trimmed of surrounding whitespace), read
//     once at startup. TB_OPERATOR_SECRET must be unset.
//
// OperatorSecret holds the resolved key material either way; OperatorSecretPath
// holds the raw path in file mode only.
func Load(lookup Lookup) (*Config, error) {
	var errs []error
	get := func(name string) string {
		v, err := requireString(lookup, name)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}

	cfg := &Config{
		DatabaseURL:         get(envDatabaseURL),
		StellarRPCURL:       get(envStellarRPCURL),
		NetworkPassphrase:   get(envNetworkPassphrase),
		PriceBookID:         get(envPriceBookID),
		StatementRegistryID: get(envStatementRegistryID),
		OperatorAddress:     get(envOperatorAddress),
	}

	if err := validateURL(envDatabaseURL, cfg.DatabaseURL, "postgres", "postgresql"); err != nil {
		errs = append(errs, err)
	}
	if err := validateURL(envStellarRPCURL, cfg.StellarRPCURL, "http", "https"); err != nil {
		errs = append(errs, err)
	}
	if err := validateStrkeyLike(envOperatorAddress, cfg.OperatorAddress, 'G'); err != nil {
		errs = append(errs, err)
	}
	if err := validateStrkeyLike(envPriceBookID, cfg.PriceBookID, 'C'); err != nil {
		errs = append(errs, err)
	}
	if err := validateStrkeyLike(envStatementRegistryID, cfg.StatementRegistryID, 'C'); err != nil {
		errs = append(errs, err)
	}

	source := get(envOperatorSecretSource)
	switch source {
	case SecretSourceEnv, SecretSourceFile:
		cfg.OperatorSecretSource = source
	case "":
		// already recorded as missing by get()
	default:
		errs = append(errs, fmt.Errorf("config: %s must be %q or %q, got %q",
			envOperatorSecretSource, SecretSourceEnv, SecretSourceFile, source))
	}

	secretRaw, secretSet := lookup(envOperatorSecret)
	secretSet = secretSet && secretRaw != ""
	pathRaw, pathSet := lookup(envOperatorSecretPath)
	pathSet = pathSet && pathRaw != ""

	switch cfg.OperatorSecretSource {
	case SecretSourceEnv:
		if secretSet {
			cfg.OperatorSecret = Secret(secretRaw)
		} else {
			errs = append(errs, fmt.Errorf("config: missing required environment variable %s", envOperatorSecret))
		}
		if pathSet {
			errs = append(errs, fmt.Errorf("config: %s must not be set when %s=%s; use %s instead",
				envOperatorSecretPath, envOperatorSecretSource, SecretSourceEnv, envOperatorSecret))
		}
	case SecretSourceFile:
		if pathSet {
			cfg.OperatorSecretPath = Secret(pathRaw)
			secret, err := readSecretFile(pathRaw)
			if err != nil {
				errs = append(errs, fmt.Errorf("config: %s: %w", envOperatorSecretPath, err))
			} else {
				cfg.OperatorSecret = secret
			}
		} else {
			errs = append(errs, fmt.Errorf("config: missing required environment variable %s", envOperatorSecretPath))
		}
		if secretSet {
			errs = append(errs, fmt.Errorf("config: %s must not be set when %s=%s; use %s instead",
				envOperatorSecret, envOperatorSecretSource, SecretSourceFile, envOperatorSecretPath))
		}
	}

	if raw, ok := lookup(envSafetyMarginLedgers); ok && raw != "" {
		v, err := parseUint32(envSafetyMarginLedgers, raw)
		if err != nil {
			errs = append(errs, err)
		} else {
			cfg.SafetyMarginLedgers = v
		}
	} else {
		cfg.SafetyMarginLedgers = defaultSafetyMarginLedgers
	}

	if raw := get(envMaxExposure); raw != "" {
		v, ok := new(big.Int).SetString(raw, 10)
		if !ok {
			errs = append(errs, fmt.Errorf("config: %s: not a valid integer: %q", envMaxExposure, raw))
		} else if v.Sign() < 0 {
			errs = append(errs, fmt.Errorf("config: %s: must not be negative, got %s", envMaxExposure, v))
		} else {
			cfg.MaxExposure = v
		}
	}

	if raw := get(envMaxExposureAge); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: %s: %w", envMaxExposureAge, err))
		} else if v <= 0 {
			errs = append(errs, fmt.Errorf("config: %s: must be positive, got %s", envMaxExposureAge, v))
		} else {
			cfg.MaxExposureAge = v
		}
	}

	if raw := get(envPeriodDuration); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: %s: %w", envPeriodDuration, err))
		} else if v <= 0 {
			errs = append(errs, fmt.Errorf("config: %s: must be positive, got %s", envPeriodDuration, v))
		} else {
			cfg.PeriodDuration = v
		}
	}

	if raw := get(envSettlerTick); raw != "" {
		v, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: %s: %w", envSettlerTick, err))
		} else if v <= 0 {
			errs = append(errs, fmt.Errorf("config: %s: must be positive, got %s", envSettlerTick, v))
		} else {
			cfg.SettlerTick = v
		}
	}

	if raw := get(envIndexerStartLedger); raw != "" {
		v, err := parseUint32(envIndexerStartLedger, raw)
		if err != nil {
			errs = append(errs, err)
		} else {
			cfg.IndexerStartLedger = v
		}
	}

	if raw := get(envCollectorAddr); raw != "" {
		if _, _, err := net.SplitHostPort(raw); err != nil {
			errs = append(errs, fmt.Errorf("config: %s: not a valid host:port: %w", envCollectorAddr, err))
		} else {
			cfg.CollectorAddr = raw
		}
	}

	if raw := get(envLogLevel); raw != "" {
		lvl, err := parseLogLevel(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("config: %s: %w", envLogLevel, err))
		} else {
			cfg.LogLevel = lvl
		}
	}

	for _, timeout := range []struct {
		name         string
		value        *time.Duration
		defaultValue time.Duration
	}{
		{envHTTPReadHeaderTimeout, &cfg.HTTPReadHeaderTimeout, defaultHTTPReadHeaderTimeout},
		{envHTTPReadTimeout, &cfg.HTTPReadTimeout, defaultHTTPReadTimeout},
		{envHTTPWriteTimeout, &cfg.HTTPWriteTimeout, defaultHTTPWriteTimeout},
		{envHTTPIdleTimeout, &cfg.HTTPIdleTimeout, defaultHTTPIdleTimeout},
	} {
		value, err := optionalPositiveDuration(lookup, timeout.name, timeout.defaultValue)
		if err != nil {
			errs = append(errs, err)
		} else {
			*timeout.value = value
		}
	}

	if raw, ok := lookup(envIndexerMaxConsecutiveFailures); !ok || raw == "" {
		cfg.IndexerMaxConsecutiveFailures = defaultIndexerMaxConsecutiveFailures
	} else {
		value, err := parseUint32(envIndexerMaxConsecutiveFailures, raw)
		if err != nil {
			errs = append(errs, err)
		} else if value == 0 {
			errs = append(errs, fmt.Errorf("config: %s: must be positive", envIndexerMaxConsecutiveFailures))
		} else {
			cfg.IndexerMaxConsecutiveFailures = value
		}
	}

	for _, addr := range []struct {
		name     string
		value    *string
		fallback string
	}{
		{envSettlerMetricsAddr, &cfg.SettlerMetricsAddr, DefaultSettlerMetricsAddr},
		{envIndexerAddr, &cfg.IndexerAddr, DefaultIndexerAddr},
	} {
		value, err := optionalHostPort(lookup, addr.name, addr.fallback)
		if err != nil {
			errs = append(errs, err)
		} else {
			*addr.value = value
		}
	}

	if value, err := optionalPositiveDuration(lookup, envIndexerTick, DefaultIndexerTick); err != nil {
		errs = append(errs, err)
	} else {
		cfg.IndexerTick = value
	}

	// The anchor token is optional, and empty means "no token configured"
	// rather than "malformed": the collector then meters and closes
	// periods but does not anchor the ones whose token it cannot derive.
	if raw, ok := lookup(envAnchorToken); ok && raw != "" {
		if err := validateStrkeyLike(envAnchorToken, raw, 'C'); err != nil {
			errs = append(errs, err)
		} else {
			cfg.AnchorToken = raw
		}
	}

	for _, tick := range []struct {
		name     string
		value    *time.Duration
		fallback time.Duration
	}{
		{envCollectorPeriodTick, &cfg.CollectorPeriodTick, DefaultCollectorPeriodTick},
		{envCollectorAnchorTick, &cfg.CollectorAnchorTick, DefaultCollectorAnchorTick},
	} {
		value, err := optionalPositiveDuration(lookup, tick.name, tick.fallback)
		if err != nil {
			errs = append(errs, err)
		} else {
			*tick.value = value
		}
	}

	if len(errs) > 0 {
		return nil, joinErrors(errs)
	}
	return cfg, nil
}

func requireString(lookup Lookup, name string) (string, error) {
	v, ok := lookup(name)
	if !ok || v == "" {
		return "", fmt.Errorf("config: missing required environment variable %s", name)
	}
	return v, nil
}

func validateURL(name, value string, allowedSchemes ...string) error {
	if value == "" {
		return nil // already reported as missing
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("config: %s: not a valid URL: %w", name, err)
	}
	if u.Host == "" {
		return fmt.Errorf("config: %s: not a valid URL, missing host: %q", name, value)
	}
	for _, s := range allowedSchemes {
		if u.Scheme == s {
			return nil
		}
	}
	return fmt.Errorf("config: %s: scheme must be one of %v, got %q", name, allowedSchemes, u.Scheme)
}

// validateStrkeyLike performs a cheap sanity check on a Stellar strkey
// address — correct leading character and length — without pulling in the
// strkey/xdr dependency this early. Full checksum validation happens where
// the stellar layer actually decodes these (see internal/stellar).
func validateStrkeyLike(name, value string, prefix byte) error {
	if value == "" {
		return nil // already reported as missing
	}
	if len(value) != 56 {
		return fmt.Errorf("config: %s: must be 56 characters, got %d: %q", name, len(value), value)
	}
	if value[0] != prefix {
		return fmt.Errorf("config: %s: must start with %q, got %q", name, string(prefix), value)
	}
	for _, r := range value {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", r) {
			return fmt.Errorf("config: %s: not valid base32, got %q", name, value)
		}
	}
	return nil
}

func parseUint32(name, raw string) (uint32, error) {
	v, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("config: %s: not a valid non-negative integer: %q", name, raw)
	}
	return uint32(v), nil
}

func optionalPositiveDuration(lookup Lookup, name string, fallback time.Duration) (time.Duration, error) {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", name, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("config: %s: must be positive, got %s", name, value)
	}
	return value, nil
}

// optionalHostPort returns the host:port held in name, or fallback when
// the variable is unset or empty. A malformed value is an error rather
// than a silent fall back to the default: a typo in a listen address
// must not quietly move a service onto a different port, and it must not
// be the port the operator did not ask for.
func optionalHostPort(lookup Lookup, name, fallback string) (string, error) {
	raw, ok := lookup(name)
	if !ok || raw == "" {
		return fallback, nil
	}
	if _, _, err := net.SplitHostPort(raw); err != nil {
		return "", fmt.Errorf("config: %s: not a valid host:port: %w", name, err)
	}
	return raw, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("must be one of debug, info, warn, error, got %q", raw)
	}
}

func readSecretFile(path string) (Secret, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return "", fmt.Errorf("secret file %s is empty", path)
	}
	return Secret(trimmed), nil
}

// joinErrors folds every validation failure into one error with the same
// message format callers already see, but preserves each failure's own
// wrap chain instead of flattening it to text: errors.Is can still match a
// sentinel one of the failures wrapped (os.ErrNotExist from a secret file
// read, say) even though all the texts are joined into one report.
func joinErrors(errs []error) error {
	return fmt.Errorf("%d configuration error(s):\n%w", len(errs), errors.Join(errs...))
}
