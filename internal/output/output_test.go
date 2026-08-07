package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/daemon"
	"github.com/danieljustus/symaira-browse/internal/exitcodes"
	"github.com/danieljustus/symaira-browse/internal/session"
)

// goldenPath resolves a golden file under testdata/golden.
func goldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", "golden", name)
}

// readGolden loads the expected envelope JSON from a golden file.
func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(goldenPath(t, name))
	if err != nil {
		t.Fatalf("read golden file %s: %v", name, err)
	}
	return content
}

// assertGolden verifies that the serialised envelope matches the golden file.
func assertGolden(t *testing.T, name string, envelope Envelope) {
	t.Helper()
	var buffer bytes.Buffer
	if err := Write(&buffer, envelope, true); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	expected := readGolden(t, name)
	if !bytes.Equal(bytes.TrimSpace(buffer.Bytes()), bytes.TrimSpace(expected)) {
		t.Fatalf("envelope does not match golden file %s\n got: %s\nwant: %s", name, buffer.Bytes(), expected)
	}
}

func TestGoldenSuccessEnvelope(t *testing.T) {
	assertGolden(t, "success.json", OK(map[string]any{"result": "ok"}, []Warning{{Kind: "note", Severity: "info", Message: "example warning"}}))
}

func TestGoldenErrorEnvelope(t *testing.T) {
	assertGolden(t, "error.json", FailureWithHint("stale_ref", "element @e7 no longer exists", "run snapshot --diff to see what changed"))
}

func TestFromErrorUsesKindMapping(t *testing.T) {
	err := exitcodes.Wrap(nil, exitcodes.ExitConflict, exitcodes.KindConflict, "element is gone")
	payload := FromError(err)
	if payload.Code != string(CodeConflict) {
		t.Fatalf("code = %q, want %q", payload.Code, CodeConflict)
	}
	if payload.Message != "element is gone" {
		t.Fatalf("message = %q", payload.Message)
	}
}

func TestFromErrorPreservesDaemonCode(t *testing.T) {
	err := daemon.NewError("stale_ref", "element @e7 no longer exists")
	payload := FromError(err)
	if payload.Code != string(CodeStaleRef) {
		t.Fatalf("code = %q, want %q", payload.Code, CodeStaleRef)
	}
}

func TestFromErrorUnknownCodeFallsBackToInternal(t *testing.T) {
	err := errors.New("plain failure")
	payload := FromError(err)
	if payload.Code != string(CodeInternal) {
		t.Fatalf("code = %q, want %q", payload.Code, CodeInternal)
	}
}

func TestFromErrorRejectsFreeFormCodes(t *testing.T) {
	err := daemon.NewError("made_up_code", "not part of the enum")
	payload := FromError(err)
	if payload.Code != string(CodeInternal) {
		t.Fatalf("code = %q, want internal fallback", payload.Code)
	}
}

func TestEveryCodeIsValidAndMapsToExitCode(t *testing.T) {
	for code := range allCodes {
		if !IsValid(string(code)) {
			t.Fatalf("code %q not reported valid", code)
		}
		// The mapping must be total: every enum member resolves to a kind and
		// a non-OK exit code.
		if ExitCodeFromCode(code) == exitcodes.ExitOK {
			t.Fatalf("code %q maps to ExitOK", code)
		}
	}
}

func TestErrorEnvelopeShape(t *testing.T) {
	var buffer bytes.Buffer
	if err := WriteError(&buffer, exitcodes.Wrap(nil, exitcodes.ExitNotFound, exitcodes.KindNotFound, "session missing"), true); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Success bool `json:"success"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &payload); err != nil {
		t.Fatalf("output = %q: %v", buffer.String(), err)
	}
	if payload.Success || payload.Error.Code != "not_found" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestWriteHumanEnvelopeVariants(t *testing.T) {
	cases := []struct {
		name     string
		envelope Envelope
		want     string
	}{
		{name: "failed without payload", envelope: Envelope{Success: false}, want: "error\n"},
		{name: "failed with message", envelope: Failure("internal", "something failed"), want: "something failed\n"},
		{name: "successful without data", envelope: OK(nil, nil), want: "ok\n"},
		{name: "successful string", envelope: OK("hello", nil), want: "hello\n"},
		{name: "successful value", envelope: OK(42, nil), want: "42\n"},
		{name: "truncation marker", envelope: OK(map[string]any{
			"truncated":       true,
			"tokens_returned": float64(90),
			"tokens_total":    float64(18400),
			"cache_id":        "out_abc",
			"hint":            "symbrowse cache get out_abc --range 40-120",
			"head":            "HEAD TEXT",
			"foot":            "FOOT TEXT",
		}, nil), want: "HEAD TEXT\n\n… [truncated: 90 of 18400 tokens] …\n\nFOOT TEXT\n\nfull output: symbrowse cache get out_abc --range 40-120\n"},
		{name: "plain map is not a marker", envelope: OK(map[string]any{"truncated": "yes"}, nil), want: "map[truncated:yes]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			if err := Write(&buffer, tc.envelope, false); err != nil {
				t.Fatal(err)
			}
			if got := buffer.String(); got != tc.want {
				t.Fatalf("human output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorStringHandlesNilReceiver(t *testing.T) {
	var err *Error
	if got := err.Error(); got != "output envelope error" {
		t.Fatalf("nil error string = %q", got)
	}
}

func TestHardStopErrorEnvelopeCarriesResumeContract(t *testing.T) {
	hardStop := &session.HardStopError{
		Code:                     session.CodeSessionUserControl,
		Message:                  "session is controlled by a human",
		RequiresUserConfirmation: true,
		ResumeHint:               "confirm takeover before retrying",
	}
	payload := ErrorEnvelope(hardStop)
	if payload.Success || payload.Error == nil || payload.Error.Code != session.CodeSessionUserControl {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.Error.Retryable == nil || *payload.Error.Retryable || payload.Error.RequiresUserConfirmation == nil || !*payload.Error.RequiresUserConfirmation || payload.Error.ResumeHint == "" {
		t.Fatalf("hard-stop metadata = %#v", payload.Error)
	}
}
