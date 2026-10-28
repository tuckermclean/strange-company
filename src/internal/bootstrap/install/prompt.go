package install

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"strange.company/sc/internal/bootstrap/identity"
)

// readLine prints prompt on stderr and reads one line of input; with secret it switches terminal echo off when stdin
// is a terminal. An end of input before any byte is errNoInput.
func (s *sess) readLine(ctx context.Context, prompt string, secret bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.errorf("%s", prompt)
	if secret {
		if f, ok := s.deps.Stdin.(*os.File); ok {
			restore := disableEcho(f)
			defer func() {
				restore()
				s.errorf("\n")
			}()
		}
	}
	line, err := s.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		if errors.Is(err, io.EOF) {
			return "", errNoInput
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// newPassphrase obtains the recovery passphrase for a new bundle: SC_RECOVERY_PASSPHRASE_FILE, else two prompts.
func (s *sess) newPassphrase(ctx context.Context) (string, error) {
	if f := s.env["SC_RECOVERY_PASSPHRASE_FILE"]; f != "" {
		p, err := identity.ReadPassphraseFile(f)
		if err != nil {
			return "", err
		}
		if err := identity.CheckPassphrase(p, s.cluster); err != nil {
			return "", err
		}
		return p, nil
	}
	if s.inv.NonInteractive {
		return "", &failure{"PASSPHRASE_REQUIRED", "no recovery passphrase: set SC_RECOVERY_PASSPHRASE_FILE, or run without --non-interactive to be asked", 5}
	}
	s.printf("Choose the recovery passphrase (at least 20 characters or 5 words, not containing the cluster name).\n")
	s.printf("It is the only way to open the recovery bundle; store it offline.\n")
	return identity.NewPassphrase(func() (string, error) {
		return s.readLine(ctx, "Recovery passphrase: ", true)
	}, s.cluster)
}

// existingPassphrase obtains the passphrase of a bundle that already exists (resume): the file, else one prompt.
func (s *sess) existingPassphrase(ctx context.Context) (string, error) {
	if s.pass != "" {
		return s.pass, nil
	}
	if f := s.env["SC_RECOVERY_PASSPHRASE_FILE"]; f != "" {
		p, err := identity.ReadPassphraseFile(f)
		if err != nil {
			return "", err
		}
		s.pass = p
		return p, nil
	}
	if s.inv.NonInteractive {
		return "", &failure{"PASSPHRASE_REQUIRED", "the recovery bundle must be opened to continue: set SC_RECOVERY_PASSPHRASE_FILE, or run without --non-interactive", 5}
	}
	p, err := s.readLine(ctx, "Recovery passphrase: ", true)
	if err != nil {
		return "", err
	}
	s.pass = p
	return p, nil
}
