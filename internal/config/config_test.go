package config

import (
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
	if cfg.OperatorSecret.Reveal() != "S"+strings.Repeat("E", 55) {
		t.Errorf("OperatorSecret.Reveal() did not round-trip")
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
