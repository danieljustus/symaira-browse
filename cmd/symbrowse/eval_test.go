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
