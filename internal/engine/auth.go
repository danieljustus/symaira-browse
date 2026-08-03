package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// LoginFields describes the detected username/password inputs of a standard
// login form. Selectors are CSS selectors usable with the interaction
// service; they are resolved fresh on every auth login so page changes can
// never redirect credentials to a wrong field.
type LoginFields struct {
	UsernameSelector string `json:"username_selector"`
	PasswordSelector string `json:"password_selector"`
	FormAction       string `json:"form_action,omitempty"`
	Found            bool   `json:"found"`
}

// DetectLoginFields finds a standard login form: a password input plus the
// closest preceding text/email input in the same form. Returns Found=false
// (not an error) when the page has no login form.
func (s *NavigationService) DetectLoginFields(ctx context.Context) (LoginFields, error) {
	result, err := s.engine.Evaluate(ctx, s.page, detectLoginFieldsExpression)
	if err != nil {
		return LoginFields{}, fmt.Errorf("detect login fields: %w", err)
	}
	if result.ExceptionText != "" {
		return LoginFields{}, errors.New(result.ExceptionText)
	}
	var fields LoginFields
	if err := json.Unmarshal(result.Value, &fields); err != nil {
		return LoginFields{}, fmt.Errorf("decode login fields: %w", err)
	}
	return fields, nil
}

// FillLoginField types a value into the field identified by selector using
// the interaction service. The value is never returned, logged or persisted
// by this call.
func (s *NavigationService) FillLoginField(ctx context.Context, selector, value string) error {
	_, err := s.Interact(ctx, InteractionRequest{
		Action:   ActionFill,
		Selector: selector,
		Value:    value,
	})
	if err != nil {
		return fmt.Errorf("fill login field %q: %w", selector, err)
	}
	return nil
}

// PressEnter submits the currently focused form field (standard login flow).
func (s *NavigationService) PressEnter(ctx context.Context) error {
	_, err := s.Interact(ctx, InteractionRequest{Action: ActionPress, Selector: "body", Key: "Enter"})
	if err != nil {
		return fmt.Errorf("press enter: %w", err)
	}
	return nil
}

const detectLoginFieldsExpression = `(function(){
	const password = document.querySelector('input[type="password"]');
	if (!password) return {found: false};
	const form = password.closest('form');
	let username = null;
	const candidates = form ? form.querySelectorAll('input[type="text"], input[type="email"], input:not([type])') : [];
	for (const input of candidates) {
		if (input === password) break;
		if (input.type === 'password') continue;
		if (!input.disabled && !input.readOnly && input.offsetParent !== null) { username = input; break; }
	}
	if (!username) {
		// Fallback: the first visible text-like input anywhere before the password.
		const all = document.querySelectorAll('input[type="text"], input[type="email"], input:not([type])');
		for (const input of all) {
			if (input.disabled || input.readOnly || input.offsetParent === null) continue;
			username = input; break;
		}
	}
	if (!username) return {found: false};
	const uid = (el) => {
		if (el.id) return '#' + CSS.escape(el.id);
		const name = el.getAttribute('name');
		if (name) return 'input[name="' + CSS.escape(name) + '"]';
		return null;
	};
	const us = uid(username) || (function(){ let i = 0; for (const el of document.querySelectorAll('input')) { if (el === username) return 'input:nth-of-type(' + (i+1) + ')'; i++; } return null; })();
	const ps = uid(password) || (function(){ let i = 0; for (const el of document.querySelectorAll('input')) { if (el === password) return 'input:nth-of-type(' + (i+1) + ')'; i++; } return null; })();
	if (!us || !ps) return {found: false};
	return {found: true, username_selector: us, password_selector: ps, form_action: form ? (form.getAttribute('action') || '') : ''};
})()`
