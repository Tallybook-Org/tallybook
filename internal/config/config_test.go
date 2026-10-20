package config

import (
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

// validEnv returns a complete, valid environment map that tests mutate from.
func validEnv() map[string]string {
	return map[string]string{
		envDatabaseURL:          "postgres://tallybook:tallybook@localhost:5432/tallybook",
		envStellarRPCURL:        "https://soroban-testnet.stellar.org",
		envNetworkPassphrase:    "Test SDF Network ; September 2015",
		envPriceBookID:          "C" + strings.Repeat("B", 55),
		envStatementRegistryID:  "C" + strings.Repeat("D", 55),
		envOperatorAddress:      "G" + strings.Repeat("A", 55),
		envOperatorSecretSource: SecretSourceEnv,
		envOperatorSecret:       "S" + strings.Repeat("E", 55),
		envSafetyMarginLedgers:  "1440",
		envMaxExposure:          "10000000",
		envMaxExposureAge:       "24h",
		envPeriodDuration:       "720h",
		envSettlerTick:          "30s",
		envIndexerStartLedger:   "4590000",
		envCollectorAddr:        ":8080",
		envLogLevel:             "info",
	}
}

func lookupFrom(m map[string]string) Lookup {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load(lookupFrom(validEnv()))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "postgres://tallybook:tallybook@localhost:5432/tallybook" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.SafetyMarginLedgers != 1440 {
		t.Errorf("SafetyMarginLedgers = %d, want 1440", cfg.SafetyMarginLedgers)
	}
	if cfg.MaxExposure.Cmp(big.NewInt(10000000)) != 0 {
		t.Errorf("MaxExposure = %s, want 10000000", cfg.MaxExposure)
	}
	if cfg.MaxExposureAge != 24*time.Hour {
		t.Errorf("MaxExposureAge = %s, want 24h", cfg.MaxExposureAge)
	}
	if cfg.PeriodDuration != 720*time.Hour {
		t.Errorf("PeriodDuration = %s, want 720h", cfg.PeriodDuration)
	}
	if cfg.SettlerTick != 30*time.Second {
		t.Errorf("SettlerTick = %s, want 30s", cfg.SettlerTick)
	}
	if cfg.IndexerStartLedger != 4590000 {
		t.Errorf("IndexerStartLedger = %d, want 4590000", cfg.IndexerStartLedger)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.HTTPReadHeaderTimeout != defaultHTTPReadHeaderTimeout || cfg.HTTPReadTimeout != defaultHTTPReadTimeout ||
		cfg.HTTPWriteTimeout != defaultHTTPWriteTimeout || cfg.HTTPIdleTimeout != defaultHTTPIdleTimeout {
		t.Errorf("HTTP timeouts = (%s, %s, %s, %s), want defaults (%s, %s, %s, %s)",
			cfg.HTTPReadHeaderTimeout, cfg.HTTPReadTimeout, cfg.HTTPWriteTimeout, cfg.HTTPIdleTimeout,
			defaultHTTPReadHeaderTimeout, defaultHTTPReadTimeout, defaultHTTPWriteTimeout, defaultHTTPIdleTimeout)
	}
	if cfg.IndexerMaxConsecutiveFailures != defaultIndexerMaxConsecutiveFailures {
		t.Errorf("IndexerMaxConsecutiveFailures = %d, want %d", cfg.IndexerMaxConsecutiveFailures, defaultIndexerMaxConsecutiveFailures)
	}
	if cfg.OperatorSecret.Reveal() != "S"+strings.Repeat("E", 55) {
		t.Errorf("OperatorSecret.Reveal() did not round-trip")
	}
}

func TestLoad_HTTPTimeoutOverrides(t *testing.T) {
	env := validEnv()
	env[envHTTPReadHeaderTimeout] = "3s"
	env[envHTTPReadTimeout] = "45s"
	env[envHTTPWriteTimeout] = "4m"
	env[envHTTPIdleTimeout] = "90s"
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.HTTPReadHeaderTimeout != 3*time.Second || cfg.HTTPReadTimeout != 45*time.Second ||
		cfg.HTTPWriteTimeout != 4*time.Minute || cfg.HTTPIdleTimeout != 90*time.Second {
		t.Errorf("HTTP timeout overrides were not applied: %+v", cfg)
	}
}

func TestLoad_IndexerFailureThreshold(t *testing.T) {
	env := validEnv()
	env[envIndexerMaxConsecutiveFailures] = "5"
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.IndexerMaxConsecutiveFailures != 5 {
		t.Errorf("IndexerMaxConsecutiveFailures = %d, want 5", cfg.IndexerMaxConsecutiveFailures)
	}
}

func TestLoad_SafetyMarginDefaultsWhenUnset(t *testing.T) {
	env := validEnv()
	delete(env, envSafetyMarginLedgers)
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.SafetyMarginLedgers != defaultSafetyMarginLedgers {
		t.Errorf("SafetyMarginLedgers = %d, want default %d", cfg.SafetyMarginLedgers, defaultSafetyMarginLedgers)
	}
}

// TestLoad_SettlerAndIndexerDefaultsWhenUnset covers the three variables
// that were fixed inside the binaries before they were configurable:
// leaving them unset must reproduce exactly what those binaries did, so
// an existing deployment that sets nothing keeps behaving the same.
func TestLoad_SettlerAndIndexerDefaultsWhenUnset(t *testing.T) {
	env := validEnv()
	delete(env, envSettlerMetricsAddr)
	delete(env, envIndexerAddr)
	delete(env, envIndexerTick)
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.SettlerMetricsAddr != DefaultSettlerMetricsAddr {
		t.Errorf("SettlerMetricsAddr = %q, want default %q", cfg.SettlerMetricsAddr, DefaultSettlerMetricsAddr)
	}
	if cfg.IndexerAddr != DefaultIndexerAddr {
		t.Errorf("IndexerAddr = %q, want default %q", cfg.IndexerAddr, DefaultIndexerAddr)
	}
	if cfg.IndexerTick != DefaultIndexerTick {
		t.Errorf("IndexerTick = %s, want default %s", cfg.IndexerTick, DefaultIndexerTick)
	}
}

// TestLoad_SettlerAndIndexerOverrides covers the same three variables
// when they are set: the point of making them configurable is that an
// operator can move a service off its default port or retune a poll
// cadence without a rebuild.
func TestLoad_SettlerAndIndexerOverrides(t *testing.T) {
	env := validEnv()
	env[envSettlerMetricsAddr] = "127.0.0.1:19101"
	env[envIndexerAddr] = "127.0.0.1:19102"
	env[envIndexerTick] = "45s"
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.SettlerMetricsAddr != "127.0.0.1:19101" {
		t.Errorf("SettlerMetricsAddr = %q, want 127.0.0.1:19101", cfg.SettlerMetricsAddr)
	}
	if cfg.IndexerAddr != "127.0.0.1:19102" {
		t.Errorf("IndexerAddr = %q, want 127.0.0.1:19102", cfg.IndexerAddr)
	}
	if cfg.IndexerTick != 45*time.Second {
		t.Errorf("IndexerTick = %s, want 45s", cfg.IndexerTick)
	}
}

// TestLoad_SettlerAndIndexerInvalidValues checks that a malformed value
// fails startup instead of quietly falling back to the default: a typo in
// a listen address must not move a service onto a port the operator did
// not ask for, and a non-positive poll interval is never what was meant.
func TestLoad_SettlerAndIndexerInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{"malformed settler metrics address", envSettlerMetricsAddr, "9101"},
		{"malformed indexer address", envIndexerAddr, "not-a-host-port"},
		{"malformed indexer tick", envIndexerTick, "5 seconds"},
		{"zero indexer tick", envIndexerTick, "0s"},
		{"negative indexer tick", envIndexerTick, "-1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			env[tt.key] = tt.value
			_, err := Load(lookupFrom(env))
			if err == nil {
				t.Errorf("Load accepted %s=%q", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Errorf("error %q does not name %s", err, tt.key)
			}
		})
	}
}

// TestLoad_CollectorTicks covers the collector's two background ticks:
// unset means the documented default, set means the operator's value, and
// a malformed one fails startup rather than quietly running at some other
// cadence.
func TestLoad_CollectorTicks(t *testing.T) {
	env := validEnv()
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.CollectorPeriodTick != DefaultCollectorPeriodTick {
		t.Errorf("CollectorPeriodTick = %s, want default %s", cfg.CollectorPeriodTick, DefaultCollectorPeriodTick)
	}
	if cfg.CollectorAnchorTick != DefaultCollectorAnchorTick {
		t.Errorf("CollectorAnchorTick = %s, want default %s", cfg.CollectorAnchorTick, DefaultCollectorAnchorTick)
	}

	env[envCollectorPeriodTick] = "30s"
	env[envCollectorAnchorTick] = "2m"
	cfg, err = Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.CollectorPeriodTick != 30*time.Second {
		t.Errorf("CollectorPeriodTick = %s, want 30s", cfg.CollectorPeriodTick)
	}
	if cfg.CollectorAnchorTick != 2*time.Minute {
		t.Errorf("CollectorAnchorTick = %s, want 2m", cfg.CollectorAnchorTick)
	}

	for _, tt := range []struct{ key, value string }{
		{envCollectorPeriodTick, "0s"},
		{envCollectorAnchorTick, "-1m"},
		{envCollectorAnchorTick, "soon"},
	} {
		bad := validEnv()
		bad[tt.key] = tt.value
		if _, err := Load(lookupFrom(bad)); err == nil {
			t.Errorf("Load accepted %s=%q", tt.key, tt.value)
		}
	}
}

// TestLoad_AnchorToken covers the one configuration value that is
// legitimately optional and empty: without it the collector meters and
// closes periods but never anchors the ones whose token it cannot
// derive, and a malformed one is rejected rather than used.
func TestLoad_AnchorToken(t *testing.T) {
	env := validEnv()
	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.AnchorToken != "" {
		t.Errorf("AnchorToken = %q, want empty when unset", cfg.AnchorToken)
	}

	env[envAnchorToken] = "C" + strings.Repeat("B", 55)
	cfg, err = Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.AnchorToken != env[envAnchorToken] {
		t.Errorf("AnchorToken = %q, want %q", cfg.AnchorToken, env[envAnchorToken])
	}

	for _, bad := range []string{"not-a-strkey", "G" + strings.Repeat("A", 55), "C" + strings.Repeat("A", 10)} {
		env[envAnchorToken] = bad
		if _, err := Load(lookupFrom(env)); err == nil {
			t.Errorf("Load accepted %s=%q", envAnchorToken, bad)
		}
	}
}

// fileSourceEnv returns a valid environment configured for
// TB_OPERATOR_SECRET_SOURCE=file, with TB_OPERATOR_SECRET removed and
// TB_OPERATOR_SECRET_PATH pointing at path.
func fileSourceEnv(path string) map[string]string {
	env := validEnv()
	env[envOperatorSecretSource] = SecretSourceFile
	delete(env, envOperatorSecret)
	env[envOperatorSecretPath] = path
	return env
}

func TestLoad_SecretSourceFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/operator.key"
	secretValue := "S" + strings.Repeat("F", 55)
	if err := writeFile(path, secretValue+"\n"); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	cfg, err := Load(lookupFrom(fileSourceEnv(path)))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}
	if cfg.OperatorSecret.Reveal() != secretValue {
		t.Errorf("OperatorSecret.Reveal() = %q, want %q", cfg.OperatorSecret.Reveal(), secretValue)
	}
	if cfg.OperatorSecretPath.Reveal() != path {
		t.Errorf("OperatorSecretPath.Reveal() = %q, want %q", cfg.OperatorSecretPath.Reveal(), path)
	}
}

func TestLoad_SecretSourceFileMissingFile(t *testing.T) {
	env := fileSourceEnv("/nonexistent/path/does/not/exist")
	_, err := Load(lookupFrom(env))
	if err == nil {
		t.Fatal("Load returned nil error for a nonexistent secret file")
	}
}

func TestLoad_SecretSourceFileMissingPath(t *testing.T) {
	env := fileSourceEnv("")
	delete(env, envOperatorSecretPath)
	_, err := Load(lookupFrom(env))
	if err == nil {
		t.Fatal("Load returned nil error with TB_OPERATOR_SECRET_PATH missing in file mode")
	}
	if !strings.Contains(err.Error(), envOperatorSecretPath) {
		t.Errorf("error %q does not name %s", err.Error(), envOperatorSecretPath)
	}
}

// TestLoad_RejectsSecretVariableForWrongMode covers the cross-field
// validation: whichever of TB_OPERATOR_SECRET / TB_OPERATOR_SECRET_PATH
// does not belong to the active TB_OPERATOR_SECRET_SOURCE must be unset, or
// Load fails naming the offending (and correct) variable — a stale
// variable left over from switching modes must not be silently ignored.
func TestLoad_RejectsSecretVariableForWrongMode(t *testing.T) {
	t.Run("path set while source=env", func(t *testing.T) {
		env := validEnv() // source=env, TB_OPERATOR_SECRET already set
		env[envOperatorSecretPath] = "/some/path"
		_, err := Load(lookupFrom(env))
		if err == nil {
			t.Fatal("Load returned nil error with TB_OPERATOR_SECRET_PATH set alongside source=env")
		}
		if !strings.Contains(err.Error(), envOperatorSecretPath) {
			t.Errorf("error %q does not name %s", err.Error(), envOperatorSecretPath)
		}
	})

	t.Run("secret set while source=file", func(t *testing.T) {
		dir := t.TempDir()
		path := dir + "/operator.key"
		if err := writeFile(path, "S"+strings.Repeat("F", 55)); err != nil {
			t.Fatalf("writeFile: %v", err)
		}
		env := fileSourceEnv(path)
		env[envOperatorSecret] = "S" + strings.Repeat("G", 55) // stale leftover
		_, err := Load(lookupFrom(env))
		if err == nil {
			t.Fatal("Load returned nil error with TB_OPERATOR_SECRET set alongside source=file")
		}
		if !strings.Contains(err.Error(), envOperatorSecret) {
			t.Errorf("error %q does not name %s", err.Error(), envOperatorSecret)
		}
	})
}

// TestLoad_MissingRequired exercises every required variable's absence
// independently, asserting the error names the specific missing variable.
func TestLoad_MissingRequired(t *testing.T) {
	required := []string{
		envDatabaseURL,
		envStellarRPCURL,
		envNetworkPassphrase,
		envPriceBookID,
		envStatementRegistryID,
		envOperatorAddress,
		envOperatorSecretSource,
		envOperatorSecret,
		envMaxExposure,
		envMaxExposureAge,
		envPeriodDuration,
		envSettlerTick,
		envIndexerStartLedger,
		envCollectorAddr,
		envLogLevel,
	}
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			delete(env, name)
			_, err := Load(lookupFrom(env))
			if err == nil {
				t.Fatalf("Load returned nil error with %s missing", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name missing variable %s", err.Error(), name)
			}
		})
	}
}

func TestLoad_InvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr string
	}{
		{
			name: "database url wrong scheme",
			mutate: func(env map[string]string) {
				env[envDatabaseURL] = "mysql://localhost/tallybook"
			},
			wantErr: envDatabaseURL,
		},
		{
			name: "stellar rpc url wrong scheme",
			mutate: func(env map[string]string) {
				env[envStellarRPCURL] = "ftp://example.com"
			},
			wantErr: envStellarRPCURL,
		},
		{
			name: "operator address wrong prefix",
			mutate: func(env map[string]string) {
				env[envOperatorAddress] = "C" + strings.Repeat("A", 55)
			},
			wantErr: envOperatorAddress,
		},
		{
			name: "operator address wrong length",
			mutate: func(env map[string]string) {
				env[envOperatorAddress] = "GSHORT"
			},
			wantErr: envOperatorAddress,
		},
		{
			name: "price book id wrong prefix",
			mutate: func(env map[string]string) {
				env[envPriceBookID] = "G" + strings.Repeat("B", 55)
			},
			wantErr: envPriceBookID,
		},
		{
			name: "operator secret source invalid",
			mutate: func(env map[string]string) {
				env[envOperatorSecretSource] = "vault"
			},
			wantErr: envOperatorSecretSource,
		},
		{
			name: "max exposure not an integer",
			mutate: func(env map[string]string) {
				env[envMaxExposure] = "not-a-number"
			},
			wantErr: envMaxExposure,
		},
		{
			name: "max exposure negative",
			mutate: func(env map[string]string) {
				env[envMaxExposure] = "-5"
			},
			wantErr: envMaxExposure,
		},
		{
			name: "max exposure age not a duration",
			mutate: func(env map[string]string) {
				env[envMaxExposureAge] = "24"
			},
			wantErr: envMaxExposureAge,
		},
		{
			name: "max exposure age zero",
			mutate: func(env map[string]string) {
				env[envMaxExposureAge] = "0s"
			},
			wantErr: envMaxExposureAge,
		},
		{
			name: "period duration not a duration",
			mutate: func(env map[string]string) {
				env[envPeriodDuration] = "thirty days"
			},
			wantErr: envPeriodDuration,
		},
		{
			name: "period duration zero",
			mutate: func(env map[string]string) {
				env[envPeriodDuration] = "0h"
			},
			wantErr: envPeriodDuration,
		},
		{
			name: "settler tick not a duration",
			mutate: func(env map[string]string) {
				env[envSettlerTick] = "thirty seconds"
			},
			wantErr: envSettlerTick,
		},
		{
			name: "http header timeout is zero",
			mutate: func(env map[string]string) {
				env[envHTTPReadHeaderTimeout] = "0s"
			},
			wantErr: envHTTPReadHeaderTimeout,
		},
		{
			name: "http write timeout is invalid",
			mutate: func(env map[string]string) {
				env[envHTTPWriteTimeout] = "slow"
			},
			wantErr: envHTTPWriteTimeout,
		},
		{
			name: "indexer failure threshold is zero",
			mutate: func(env map[string]string) {
				env[envIndexerMaxConsecutiveFailures] = "0"
			},
			wantErr: envIndexerMaxConsecutiveFailures,
		},
		{
			name: "indexer start ledger not a uint32",
			mutate: func(env map[string]string) {
				env[envIndexerStartLedger] = "-1"
			},
			wantErr: envIndexerStartLedger,
		},
		{
			name: "collector addr invalid",
			mutate: func(env map[string]string) {
				env[envCollectorAddr] = "not a host port"
			},
			wantErr: envCollectorAddr,
		},
		{
			name: "log level invalid",
			mutate: func(env map[string]string) {
				env[envLogLevel] = "verbose"
			},
			wantErr: envLogLevel,
		},
		{
			name: "safety margin ledgers not a uint32",
			mutate: func(env map[string]string) {
				env[envSafetyMarginLedgers] = "-1440"
			},
			wantErr: envSafetyMarginLedgers,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := validEnv()
			tt.mutate(env)
			_, err := Load(lookupFrom(env))
			if err == nil {
				t.Fatalf("Load returned nil error, want error mentioning %s", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %s", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoad_ReportsAllErrorsAtOnce(t *testing.T) {
	env := map[string]string{} // everything missing
	_, err := Load(lookupFrom(env))
	if err == nil {
		t.Fatal("Load returned nil error with empty environment")
	}
	for _, name := range []string{envDatabaseURL, envStellarRPCURL, envOperatorAddress} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("combined error %q missing %s", err.Error(), name)
		}
	}
}

// TestConfig_StringRedactsSecret is the redaction test required by the
// system prompt: the config's String() method must never emit the operator
// secret, in any of its forms (Secret's zero-arg String and Reveal, %s, %v,
// %+v via fmt, or a plain string search for the raw value).
func TestConfig_StringRedactsSecret(t *testing.T) {
	env := validEnv()
	secretValue := env[envOperatorSecret]

	cfg, err := Load(lookupFrom(env))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	for _, rendered := range []string{
		cfg.String(),
		fmt.Sprintf("%s", cfg), //lint:ignore S1025 deliberately exercising the %s verb path, not just String()
		fmt.Sprintf("%v", cfg),
		fmt.Sprintf("%+v", cfg),
	} {
		if strings.Contains(rendered, secretValue) {
			t.Errorf("rendered config leaks operator secret: %q", rendered)
		}
		if !strings.Contains(rendered, "[REDACTED]") {
			t.Errorf("rendered config does not show redaction marker: %q", rendered)
		}
	}
}

// TestConfig_StringRedactsSecretPath is TestConfig_StringRedactsSecret's
// counterpart for file mode: the raw TB_OPERATOR_SECRET_PATH value, and
// the file's contents resolved into OperatorSecret, must both stay out of
// String()'s output.
func TestConfig_StringRedactsSecretPath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/very-identifying-operator-key-path.pem"
	secretValue := "S" + strings.Repeat("H", 55)
	if err := writeFile(path, secretValue); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	cfg, err := Load(lookupFrom(fileSourceEnv(path)))
	if err != nil {
		t.Fatalf("Load returned unexpected error: %v", err)
	}

	rendered := cfg.String()
	if strings.Contains(rendered, path) {
		t.Errorf("rendered config leaks the operator secret path: %q", rendered)
	}
	if strings.Contains(rendered, secretValue) {
		t.Errorf("rendered config leaks the operator secret: %q", rendered)
	}
}

func ExampleSecret_Reveal() {
	s := Secret("S1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")
	fmt.Println(s.Reveal())
	// Output:
	// S1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz
}

func TestSecret_RedactsUnderAllFormatVerbs(t *testing.T) {
	s := Secret("super-secret-value")
	for _, rendered := range []string{
		s.String(),
		fmt.Sprintf("%s", s), //lint:ignore S1025 deliberately exercising the %s verb path, not just String()
		fmt.Sprintf("%v", s),
		fmt.Sprintf("%#v", s),
	} {
		if strings.Contains(rendered, "super-secret-value") {
			t.Errorf("Secret leaked under formatting: %q", rendered)
		}
	}
	if s.Reveal() != "super-secret-value" {
		t.Errorf("Reveal() = %q, want original value", s.Reveal())
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

// ExampleLoad shows both halves of Load's contract. The accepted input is
// the complete valid environment the rest of this file's tests use; the
// rejection is the file secret source pointing at a path that does not
// exist, because Load reads that file once at startup (see its doc comment
// on the two secret sources). The wrapped read error preserves the
// underlying os.ErrNotExist, so the example identifies the sentinel with
// errors.Is rather than by comparing the message. The other rejection
// classes — a missing variable, a malformed URL or strkey, a secret
// variable left over from the other source — are covered by
// TestLoad_MissingRequired, TestLoad_InvalidValues, and
// TestLoad_RejectsSecretVariableForWrongMode.
func ExampleLoad() {
	// Accepted input: every required variable set, key supplied inline
	// (source=env).
	cfg, err := Load(lookupFrom(validEnv()))
	fmt.Println("accepted:", err == nil && cfg != nil)

	// Rejected input: source=file with TB_OPERATOR_SECRET_PATH pointing
	// at a file that does not exist, and TB_OPERATOR_SECRET unset as file
	// mode requires.
	_, err = Load(lookupFrom(fileSourceEnv("/nonexistent/tallybook/operator.key")))
	fmt.Println("rejected:", errors.Is(err, os.ErrNotExist))

	// Output:
	// accepted: true
	// rejected: true
}

// ExampleLoadEnv shows the same two halves as ExampleLoad through the real
// process environment LoadEnv reads: the complete valid environment is
// accepted, and the file secret source pointing at a nonexistent path is
// rejected with an error matching os.ErrNotExist. LoadEnv resolves through
// os.LookupEnv, which cannot be injected, so the example sets and restores
// the process environment itself; every other test injects a map instead
// (see Lookup's doc comment).
func ExampleConfig_String() {
	cfg := &Config{
		DatabaseURL:          "postgres://tallybook:tallybook@localhost:5432/tallybook",
		StellarRPCURL:        "https://soroban-testnet.stellar.org",
		NetworkPassphrase:    "Test SDF Network ; September 2015",
		PriceBookID:          "CBAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		StatementRegistryID:  "CDAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		OperatorAddress:      "GAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB7JZX",
		OperatorSecretSource: "env",
		OperatorSecret:       "SXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
		SafetyMarginLedgers:  1440,
		MaxExposure:          big.NewInt(10000000),
		MaxExposureAge:       24 * time.Hour,
		PeriodDuration:       720 * time.Hour,
		SettlerTick:          30 * time.Second,
		IndexerStartLedger:   4590000,
		CollectorAddr:        ":8080",
		LogLevel:             slog.LevelInfo,
		// The HTTP server timeouts and the indexer failure budget are set
		// here to the same defaults .env.example documents, so the example
		// shows every field rather than leaving the newest ones at zero.
		HTTPReadHeaderTimeout:         5 * time.Second,
		HTTPReadTimeout:               2 * time.Minute,
		HTTPWriteTimeout:              5 * time.Minute,
		HTTPIdleTimeout:               2 * time.Minute,
		IndexerMaxConsecutiveFailures: 3,
		// Likewise for the settler's metrics address, the indexer's health
		// address, and the indexer's poll interval.
		SettlerMetricsAddr: ":9101",
		IndexerAddr:        ":9102",
		IndexerTick:        5 * time.Second,
		// The anchor token is the one field that is legitimately empty in
		// a default deployment: without it the collector meters and closes
		// periods but does not anchor the ones whose token it cannot
		// derive.
		AnchorToken: "",
		// The collector's two background ticks, at their defaults.
		CollectorPeriodTick: time.Minute,
		CollectorAnchorTick: 5 * time.Minute,
	}
	fmt.Println(cfg.String())
	// Output:
	// Config{DatabaseURL:postgres://tallybook:tallybook@localhost:5432/tallybook StellarRPCURL:https://soroban-testnet.stellar.org NetworkPassphrase:Test SDF Network ; September 2015 PriceBookID:CBAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA StatementRegistryID:CDAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA OperatorAddress:GAAACAQDAQCQMBYIBEFAWDANBYHRAEISCMKBKFQXDAMRUGY4DUPB7JZX OperatorSecretSource:env OperatorSecret:[REDACTED] OperatorSecretPath:[REDACTED] SafetyMarginLedgers:1440 MaxExposure:10000000 MaxExposureAge:24h0m0s PeriodDuration:720h0m0s SettlerTick:30s IndexerStartLedger:4590000 CollectorAddr::8080 LogLevel:INFO HTTPReadHeaderTimeout:5s HTTPReadTimeout:2m0s HTTPWriteTimeout:5m0s HTTPIdleTimeout:2m0s IndexerMaxConsecutiveFailures:3 SettlerMetricsAddr::9101 IndexerAddr::9102 IndexerTick:5s AnchorToken: CollectorPeriodTick:1m0s CollectorAnchorTick:5m0s}
}

func ExampleLoadEnv() {
	restore := setProcessEnv(validEnv())
	defer restore()

	cfg, err := LoadEnv()
	fmt.Println("accepted:", err == nil && cfg != nil)

	// Rejected input: switch to the file secret source and point it at a
	// path that does not exist.
	os.Setenv(envOperatorSecretSource, SecretSourceFile)
	os.Unsetenv(envOperatorSecret)
	os.Setenv(envOperatorSecretPath, "/nonexistent/tallybook/operator.key")

	_, err = LoadEnv()
	fmt.Println("rejected:", errors.Is(err, os.ErrNotExist))

	// Output:
	// accepted: true
	// rejected: true
}

// setProcessEnv applies env to the process environment and returns a
// function restoring each key to its previous value (or unsetting it when
// it had none). ExampleLoadEnv is its only caller: it is the one place a
// test has to exercise os.LookupEnv rather than an injected Lookup. The
// Setenv/Unsetenv error returns are discarded deliberately — the documented
// failure conditions (a malformed key or value) cannot occur for these
// non-empty constant keys — and any silent failure would surface
// immediately as the example's Output not matching.
func setProcessEnv(env map[string]string) func() {
	previous := make(map[string]*string, len(env))
	for key, value := range env {
		if old, ok := os.LookupEnv(key); ok {
			previous[key] = &old
		} else {
			previous[key] = nil
		}
		_ = os.Setenv(key, value)
	}
	return func() {
		for key, old := range previous {
			if old == nil {
				_ = os.Unsetenv(key)
				continue
			}
			_ = os.Setenv(key, *old)
		}
	}
}

// ExampleSecret_GoString demonstrates the GoString method of Secret,
// which returns [REDACTED] to prevent accidental secret leakage in debugging.
func ExampleSecret_GoString() {
	s := Secret("S1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	fmt.Println(s.GoString())
	// Output:
	// [REDACTED]
}
