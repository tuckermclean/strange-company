package credentials_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tuckermclean/strange-company/control-plane/internal/credentials"
)

const secretValue = "sk-super-secret-do-not-log"

func dirWith(t *testing.T, secret, key, value string) credentials.Dir {
	t.Helper()
	root := t.TempDir()
	if secret != "" {
		if err := os.MkdirAll(filepath.Join(root, secret), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, secret, key), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return credentials.Dir(root)
}

func TestReadReturnsTheValue(t *testing.T) {
	d := dirWith(t, "deepseek-credentials", "api-key", secretValue)

	got, err := d.Read("deepseek-credentials", "api-key")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != secretValue {
		t.Fatalf("value = %q", got)
	}
}

// A projected Secret routinely ends with a newline, and an operator editing one
// by hand leaves spaces. Sending either to a provider produces an
// authentication failure that looks nothing like its cause.
func TestReadTrimsSurroundingWhitespace(t *testing.T) {
	for _, raw := range []string{secretValue + "\n", "  " + secretValue + "  ", secretValue + "\r\n"} {
		d := dirWith(t, "s", "k", raw)
		got, err := d.Read("s", "k")
		if err != nil {
			t.Fatalf("Read(%q): %v", raw, err)
		}
		if got != secretValue {
			t.Fatalf("Read(%q) = %q, want it trimmed", raw, got)
		}
	}
}

// Missing and empty are different operator mistakes -- one forgot to project
// the Secret, the other projected an unset value -- and telling them apart is
// the difference between a five-second fix and an afternoon.
func TestAMissingCredentialIsDistinctFromAnEmptyOne(t *testing.T) {
	missing := dirWith(t, "", "", "")
	if _, err := missing.Read("nope", "api-key"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("missing: error = %v, want ErrNotFound", err)
	}

	empty := dirWith(t, "s", "k", "   \n")
	if _, err := empty.Read("s", "k"); !errors.Is(err, credentials.ErrEmpty) {
		t.Fatalf("empty: error = %v, want ErrEmpty", err)
	}
}

// Errors are logged. A credential in one is a credential in the log store, the
// terminal scrollback and whatever ships logs onward.
func TestErrorsNeverContainTheValue(t *testing.T) {
	d := dirWith(t, "s", "k", secretValue)

	// A read that succeeds, then one that fails on the same directory: no
	// error path may quote what it read.
	if _, err := d.Read("s", "k"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ secret, key string }{{"s", "missing"}, {"missing", "k"}} {
		_, err := d.Read(tc.secret, tc.key)
		if err == nil {
			t.Fatalf("Read(%q,%q) unexpectedly succeeded", tc.secret, tc.key)
		}
		if strings.Contains(err.Error(), secretValue) {
			t.Fatalf("error leaks the credential: %v", err)
		}
	}
}

// policy files are operator-editable. A Secret name of "../../var/run/secrets"
// must not become a file read outside the credentials directory.
func TestPathTraversalIsRefused(t *testing.T) {
	d := dirWith(t, "s", "k", secretValue)

	for _, tc := range []struct{ name, secret, key string }{
		{"dotdot in secret", "../etc", "passwd"},
		{"dotdot in key", "s", "../../etc/passwd"},
		{"separator in secret", "a/b", "k"},
		{"separator in key", "s", "a/b"},
		{"absolute secret", "/etc", "passwd"},
		{"empty secret", "", "k"},
		{"empty key", "s", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.Read(tc.secret, tc.key); !errors.Is(err, credentials.ErrInvalidRef) {
				t.Fatalf("error = %v, want ErrInvalidRef", err)
			}
		})
	}
}

// An unconfigured directory must fail like a missing credential rather than
// panicking or reading the process's working directory.
func TestAnUnsetDirectoryReadsNothing(t *testing.T) {
	var d credentials.Dir
	if _, err := d.Read("s", "k"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
