package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/output"
)

func newSetCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "set",
		Short:   "Apply session-wide emulation settings (viewport, device, geo, offline, headers, media, user-agent)",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newSetViewportCommand(&session))
	command.AddCommand(newSetDeviceCommand(&session))
	command.AddCommand(newSetGeoCommand(&session))
	command.AddCommand(newSetOfflineCommand(&session))
	command.AddCommand(newSetHeadersCommand(&session))
	command.AddCommand(newSetMediaCommand(&session))
	command.AddCommand(newSetUserAgentCommand(&session))
	return command
}

func newSetViewportCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "viewport <width> <height> [scale]",
		Short: "Override the viewport size and device scale factor",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			width, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return invalidArgs("width: %v", err)
			}
			height, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil {
				return invalidArgs("height: %v", err)
			}
			var scale float64
			if len(args) == 3 {
				scale, err = strconv.ParseFloat(args[2], 64)
				if err != nil {
					return invalidArgs("scale: %v", err)
				}
			}
			request := map[string]any{"width": width, "height": height, "scale": scale}
			return sendSetFrame(cmd, *session, "set.viewport", request)
		},
	}
	return command
}

func newSetDeviceCommand(session *string) *cobra.Command {
	var list bool
	command := &cobra.Command{
		Use:   "device <name>",
		Short: "Apply a named device profile (run `set device --list` for the data table)",
		Args: func(cmd *cobra.Command, args []string) error {
			if list {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if list {
				devices, err := deviceList()
				if err != nil {
					return err
				}
				if structuredOutput(cmd) {
					return writeEnvelope(cmd, output.OK(map[string]any{"devices": devices}, nil))
				}
				for _, name := range devices {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), name)
				}
				return nil
			}
			request := map[string]any{"name": args[0]}
			return sendSetFrame(cmd, *session, "set.device", request)
		},
	}
	command.Flags().BoolVar(&list, "list", false, "list available device names")
	return command
}

func newSetGeoCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "geo <latitude> <longitude>",
		Short: "Override the geolocation",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			latitude, err := strconv.ParseFloat(args[0], 64)
			if err != nil {
				return fmt.Errorf("latitude: %w", err)
			}
			longitude, err := strconv.ParseFloat(args[1], 64)
			if err != nil {
				return fmt.Errorf("longitude: %w", err)
			}
			request := map[string]any{"latitude": latitude, "longitude": longitude}
			return sendSetFrame(cmd, *session, "set.geo", request)
		},
	}
	return command
}

func newSetOfflineCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "offline [on|off]",
		Short: "Emulate offline (default: on)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			offline := true
			if len(args) == 1 {
				switch strings.ToLower(args[0]) {
				case "on":
					offline = true
				case "off":
					offline = false
				default:
					return invalidArgs("offline expects on or off, got %q", args[0])
				}
			}
			request := map[string]any{"offline": offline}
			return sendSetFrame(cmd, *session, "set.offline", request)
		},
	}
	return command
}

func newSetHeadersCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "headers <json>",
		Short: "Override per-request headers (Authorization/Cookie headers are rejected)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var headers map[string]string
			if err := json.Unmarshal([]byte(args[0]), &headers); err != nil {
				return invalidArgs("headers must be a JSON object: %v", err)
			}
			if len(headers) == 0 {
				return invalidArgs("headers object must not be empty")
			}
			request := map[string]any{"headers": headers}
			return sendSetFrame(cmd, *session, "set.headers", request)
		},
	}
	return command
}

func newSetMediaCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "media <dark|light>",
		Short: "Emulate the prefers-color-scheme media feature",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var dark bool
			switch strings.ToLower(args[0]) {
			case "dark":
				dark = true
			case "light":
				dark = false
			default:
				return invalidArgs("media expects dark or light, got %q", args[0])
			}
			request := map[string]any{"dark": dark}
			return sendSetFrame(cmd, *session, "set.media", request)
		},
	}
	return command
}

func newSetUserAgentCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "user-agent <value>",
		Short: "Override the user agent string",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"user_agent": args[0]}
			return sendSetFrame(cmd, *session, "set.user-agent", request)
		},
	}
	return command
}

func sendSetFrame(cmd *cobra.Command, session, command string, request map[string]any) error {
	payload, _ := json.Marshal(request)
	response, err := daemonRequest(cmd.Context(), session, command, payload)
	if err != nil {
		return err
	}
	if !response.Success {
		return responseError(response)
	}
	if structuredOutput(cmd) {
		return writeEnvelopeFromResponse(cmd, response)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
	return err
}

// deviceList returns the names of all devices from the embedded data table.
func deviceList() ([]string, error) {
	devices, err := engine.Devices()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, device.Name)
	}
	return names, nil
}
