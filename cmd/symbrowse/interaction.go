package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

const selectorDocumentation = `Accepted selector forms:
  - CSS selector (e.g. "button.submit", "#username", "input[name='q']")
  - Stable @eN ref from snapshot (e.g. "@e1", "@e2")
  - Role/name pair as supported by the engine (e.g. role and accessible name)`

func interactionLong(action engine.InteractionAction) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s performs the %s interaction on the targeted element.\n\n", action, action)
	b.WriteString(selectorDocumentation)
	b.WriteString("\n\nOptional [value] argument:\n")
	switch action {
	case engine.ActionFill:
		b.WriteString("  The text value to fill into the input, replacing any existing content.")
	case engine.ActionType:
		b.WriteString("  The text value to type into the element, appending to any existing content.")
	case engine.ActionPress:
		b.WriteString("  The keyboard key to press (e.g. \"Enter\", \"Tab\", \"Escape\", \"ArrowDown\").")
	case engine.ActionSelect:
		b.WriteString("  The value or label of the option to select from the drop-down.")
	case engine.ActionScroll:
		b.WriteString("  The vertical scroll amount in pixels (positive for down, negative for up).")
	default:
		b.WriteString("  Not used for this interaction.")
	}
	return b.String()
}

func interactionShort(action engine.InteractionAction) string {
	switch action {
	case engine.ActionClick:
		return "Click an element matching a selector or @ref"
	case engine.ActionDoubleClick:
		return "Double-click an element matching a selector or @ref"
	case engine.ActionFill:
		return "Fill an input element, replacing its content"
	case engine.ActionType:
		return "Type text into an element, appending to its content"
	case engine.ActionPress:
		return "Press a keyboard key on an element"
	case engine.ActionHover:
		return "Hover over an element matching a selector or @ref"
	case engine.ActionFocus:
		return "Focus an element matching a selector or @ref"
	case engine.ActionSelect:
		return "Select an option from a drop-down element"
	case engine.ActionCheck:
		return "Check a checkbox or radio element"
	case engine.ActionUncheck:
		return "Uncheck a checkbox element"
	case engine.ActionScroll:
		return "Scroll the page or an element by pixel amount"
	case engine.ActionScrollIntoView:
		return "Scroll an element into the visible viewport"
	default:
		return fmt.Sprintf("Perform %s interaction", action)
	}
}

func newInteractionCommands() []*cobra.Command {
	actions := []engine.InteractionAction{engine.ActionClick, engine.ActionDoubleClick, engine.ActionFill, engine.ActionType, engine.ActionPress, engine.ActionHover, engine.ActionFocus, engine.ActionSelect, engine.ActionCheck, engine.ActionUncheck, engine.ActionScroll, engine.ActionScrollIntoView}
	commands := make([]*cobra.Command, 0, len(actions))
	for _, action := range actions {
		commands = append(commands, newInteractionCommand(action))
	}
	return commands
}

func newInteractionCommand(action engine.InteractionAction) *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDCore,
		Use:     string(action) + " <selector> [value]",
		Short:   interactionShort(action),
		Long:    interactionLong(action),
		Args: func(cmd *cobra.Command, args []string) error {
			max := 1
			if action == engine.ActionFill || action == engine.ActionType || action == engine.ActionPress || action == engine.ActionSelect || action == engine.ActionScroll {
				max = 2
			}
			if len(args) < 1 || len(args) > max {
				return fmt.Errorf("%s requires a selector and optional value", action)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			req := engine.InteractionRequest{Action: action, Selector: args[0]}
			if len(args) == 2 {
				switch action {
				case engine.ActionScroll:
					amount, err := strconv.ParseInt(args[1], 10, 64)
					if err != nil {
						return fmt.Errorf("scroll amount: %w", err)
					}
					req.Amount = amount
				case engine.ActionPress:
					req.Key = args[1]
				default:
					req.Value = args[1]
				}
			}
			payload, err := json.Marshal(req)
			if err != nil {
				return err
			}
			response, err := request(cmd.Context(), session, string(action), payload)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			return writeDaemonResponse(cmd, response, false)
		},
	}
	command.Flags().StringVar(&session, "session", "default", "session name")
	return command
}
