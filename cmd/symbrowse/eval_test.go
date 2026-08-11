package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestEvalExpressionFromArgs(t *testing.T) {
	command := &cobra.Command{}
	command.Flags().Bool("stdin", false, "")
	command.Flags().BoolP("base64", "b", false, "")
	expression, err := evalExpression(command, []string{"1+1"})
	if err != nil || expression != "1+1" {
		t.Fatalf("expression=%q err=%v", expression, err)
	}
}

func TestEvalExpressionBase64(t *testing.T) {
	command := &cobra.Command{}
	command.Flags().Bool("stdin", false, "")
	command.Flags().BoolP("base64", "b", false, "")
	if err := command.Flags().Set("base64", "true"); err != nil {
		t.Fatal(err)
	}
	expression, err := evalExpression(command, []string{"ZG9jdW1lbnQudGl0bGU="}) // document.title
	if err != nil || expression != "document.title" {
		t.Fatalf("expression=%q err=%v", expression, err)
	}
}

func TestEvalExpressionFromStdin(t *testing.T) {
	command := &cobra.Command{}
	command.Flags().Bool("stdin", false, "")
	command.Flags().BoolP("base64", "b", false, "")
	if err := command.Flags().Set("stdin", "true"); err != nil {
		t.Fatal(err)
	}
	command.SetIn(strings.NewReader("location.href"))
	expression, err := evalExpression(command, nil)
	if err != nil || expression != "location.href" {
		t.Fatalf("expression=%q err=%v", expression, err)
	}
}

func TestEvalExpressionRequiresInput(t *testing.T) {
	command := &cobra.Command{}
	command.Flags().Bool("stdin", false, "")
	command.Flags().BoolP("base64", "b", false, "")
	if _, err := evalExpression(command, nil); err == nil {
		t.Fatal("expected an error without an expression")
	}
}

func TestPrintEvalResult(t *testing.T) {
	cases := []struct {
		name    string
		data    any
		want    string
		wantErr string
	}{
		{
			name:    "uncaught exception becomes an error",
			data:    map[string]any{"exception_text": "ReferenceError: foo is not defined"},
			wantErr: "eval threw: ReferenceError: foo is not defined",
		},
		{
			name: "missing value prints undefined",
			data: map[string]any{},
			want: "undefined\n",
		},
		{
			name: "null value prints undefined",
			data: map[string]any{"value": nil},
			want: "undefined\n",
		},
		{
			name: "typed nil marshals to null",
			data: map[string]any{"value": (*int)(nil)},
			want: "null\n",
		},
		{
			name: "string value is printed as JSON",
			data: map[string]any{"value": "hello"},
			want: "\"hello\"\n",
		},
		{
			name: "number value",
			data: map[string]any{"value": 42},
			want: "42\n",
		},
		{
			name: "boolean value",
			data: map[string]any{"value": true},
			want: "true\n",
		},
		{
			name: "object value",
			data: map[string]any{"value": map[string]any{"a": 1}},
			want: "{\"a\":1}\n",
		},
		{
			name: "array value",
			data: map[string]any{"value": []any{1, 2}},
			want: "[1,2]\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command, buffer := newOutputCommand(t)
			err := printEvalResult(command, tc.data)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("printEvalResult returned an error: %v", err)
			}
			if got := buffer.String(); got != tc.want {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
