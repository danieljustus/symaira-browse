package daemon

import (
	"context"
	"errors"
	"fmt"
)

// AuthRuntime implements credential login through symvault. Credentials are
// resolved in memory, typed into the detected fields via CDP and never
// returned, logged or persisted (issue B-39).
type AuthRuntime struct {
	nav   *NavigationRuntime
	vault *VaultResolver
}

// NewAuthRuntime creates an auth bridge for one navigation runtime.
func NewAuthRuntime(nav *NavigationRuntime, vault *VaultResolver) *AuthRuntime {
	if vault == nil {
		vault = NewVaultResolver()
	}
	return &AuthRuntime{nav: nav, vault: vault}
}

// LoginResult is the stable outcome of auth login. It deliberately contains
// no credential material.
type LoginResult struct {
	Status      string `json:"status"` // logged_in | no_form | failed
	URL         string `json:"url"`
	UsernameSet bool   `json:"username_set"`
	PasswordSet bool   `json:"password_set"`
	Hint        string `json:"hint,omitempty"`
}

// Login resolves the vault entry, navigates to the target URL when given,
// detects the login form and types the credentials. Errors are redacted so
// the password never reaches the caller, the journal or the log.
func (r *AuthRuntime) Login(ctx context.Context, session, entry, url string) (LoginResult, error) {
	creds, err := r.vault.Resolve(ctx, entry)
	if err != nil {
		if errors.Is(err, ErrVaultUnavailable) {
			return LoginResult{}, errors.New("symvault is not installed; install symvault and add the credential entry (no plaintext fallback is provided)")
		}
		return LoginResult{}, fmt.Errorf("resolve vault entry: %w", err)
	}
	service, err := r.nav.service(ctx, session)
	if err != nil {
		return LoginResult{}, err
	}
	if url != "" {
		if _, err := service.Open(ctx, url); err != nil {
			return LoginResult{}, fmt.Errorf("open %q: %w", url, err)
		}
	}
	fields, err := service.DetectLoginFields(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if !fields.Found {
		return LoginResult{Status: "no_form"}, errors.New("no login form detected on the current page")
	}
	result := LoginResult{Status: "logged_in", UsernameSet: true, PasswordSet: true}
	if err := service.FillLoginField(ctx, fields.UsernameSelector, creds.Username); err != nil {
		result.Status = "failed"
		return result, redactedLoginError(err, creds)
	}
	if err := service.FillLoginField(ctx, fields.PasswordSelector, creds.Password); err != nil {
		result.Status = "failed"
		return result, redactedLoginError(err, creds)
	}
	currentURL, err := service.Origin(ctx)
	if err == nil {
		result.URL = currentURL
	}
	// Zero the in-memory credential copies as early as possible.
	creds.Username, creds.Password = "", ""
	return result, nil
}

// redactedLoginError strips credential values from interaction errors.
func redactedLoginError(err error, creds VaultCredentials) error {
	message := redactSecrets(err.Error(), creds.Password, creds.Username)
	return errors.New(message)
}

// Handle executes auth frames.
func (r *AuthRuntime) Handle(ctx context.Context, frame Frame) (any, []Warning, error) {
	switch frame.Cmd {
	case "auth.login":
		var request struct {
			Entry string `json:"entry"`
			URL   string `json:"url,omitempty"`
		}
		if err := decodeArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		if request.Entry == "" {
			return nil, nil, errors.New("vault entry is required")
		}
		result, err := r.Login(ctx, frame.Session, request.Entry, request.URL)
		if err != nil {
			return nil, nil, err
		}
		return result, nil, nil
	default:
		return nil, nil, errors.New("unknown auth command")
	}
}

// riskClassOf reports the risk class of a command. auth.login is credential;
// everything else in the auth namespace is read. This is the classification
// hook consumed by the journal and the policy engine (issue B-43).
func riskClassOf(command string) string {
	if command == "auth.login" {
		return "credential"
	}
	return "read"
}
