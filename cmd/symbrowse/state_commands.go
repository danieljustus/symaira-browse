package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/output"
)

func newStateCommands() []*cobra.Command {
	return []*cobra.Command{newCookiesCommand(), newStorageCommand()}
}

func newCookiesCommand() *cobra.Command {
	var session, reveal string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "cookies",
		Short:   "Inspect and manage cookies of the current page origin",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.PersistentFlags().StringVar(&reveal, "reveal", "", "show cookie values (default: masked); accepts a comma-separated allowlist of cookie names or \"all\"")
	command.AddCommand(newCookiesListCommand(&session, &reveal))
	command.AddCommand(newCookiesSetCommand(&session))
	command.AddCommand(newCookiesClearCommand(&session))
	return command
}

func newCookiesListCommand(session, reveal *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "List cookies visible to the current page",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			response, err := daemonRequest(cmd.Context(), *session, "cookies.list", nil)
			if err != nil {
				return err
			}
			if !response.Success {
				return responseError(response)
			}
			if structuredOutput(cmd) {
				data := maskCookiePayload(response.Data, *reveal)
				return writeEnvelope(cmd, output.OK(data, nil))
			}
			payload := cookieListFromResponse(response.Data)
			for _, cookie := range payload.Cookies {
				value := cookie.Value
				if !revealCookie(cookie.Name, *reveal) {
					value = maskSecret(value)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", cookie.Name, value, cookie.Domain, cookie.Path, cookieFlags(cookie))
			}
			return nil
		},
	}
	return command
}

func newCookiesSetCommand(session *string) *cobra.Command {
	var curlFile, domain, path, url string
	var secure, httpOnly bool
	command := &cobra.Command{
		Use:   "set <name> <value>",
		Short: "Set a cookie (or import cookies from a curl cookie jar with --curl)",
		Args: func(cmd *cobra.Command, args []string) error {
			if curlFile != "" {
				return cobra.ExactArgs(0)(cmd, args)
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if curlFile != "" {
				return importCurlCookies(cmd, *session, curlFile)
			}
			request := map[string]any{
				"cookie": engine.Cookie{Name: args[0], Value: args[1], Domain: domain, Path: path, Secure: secure, HTTPOnly: httpOnly},
				"url":    url,
			}
			payload, _ := json.Marshal(request)
			response, err := daemonRequest(cmd.Context(), *session, "cookies.set", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return err
		},
	}
	command.Flags().StringVar(&curlFile, "curl", "", "import cookies from a curl cookie jar (Netscape format) file")
	command.Flags().StringVar(&domain, "domain", "", "cookie domain (default: derived from the page URL)")
	command.Flags().StringVar(&path, "path", "/", "cookie path")
	command.Flags().StringVar(&url, "url", "", "URL scope for the cookie (default: current page URL)")
	command.Flags().BoolVar(&secure, "secure", false, "mark the cookie as secure-only")
	command.Flags().BoolVar(&httpOnly, "http-only", false, "mark the cookie as HTTP-only")
	return command
}

func newCookiesClearCommand(session *string) *cobra.Command {
	var url string
	command := &cobra.Command{
		Use:   "clear <name>",
		Short: "Delete one cookie by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"name": args[0], "url": url}
			payload, _ := json.Marshal(request)
			response, err := daemonRequest(cmd.Context(), *session, "cookies.clear", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return err
		},
	}
	command.Flags().StringVar(&url, "url", "", "URL scope of the cookie (default: current page URL)")
	return command
}

func newStorageCommand() *cobra.Command {
	var session string
	command := &cobra.Command{
		GroupID: groupIDState,
		Use:     "storage",
		Short:   "Inspect and manage per-origin web storage",
		Args:    cobra.NoArgs,
	}
	command.PersistentFlags().StringVar(&session, "session", "default", "session name")
	command.AddCommand(newStorageGetCommand(&session))
	command.AddCommand(newStorageSetCommand(&session))
	command.AddCommand(newStorageClearCommand(&session))
	return command
}

func newStorageGetCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "get <local|session> [key]",
		Short: "Read web storage values for the current origin",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"kind": args[0]}
			payload, _ := json.Marshal(request)
			response, err := daemonRequest(cmd.Context(), *session, "storage.list", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			payloadData := storageListFromResponse(response.Data)
			if len(args) == 2 {
				value, ok := payloadData.Items[args[1]]
				if !ok {
					return fmt.Errorf("key %q not found in %s storage of %s", args[1], args[0], payloadData.Origin)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), value)
				return err
			}
			for key, value := range payloadData.Items {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", key, value)
			}
			return nil
		},
	}
	return command
}

func newStorageSetCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "set <local|session> <key> <value>",
		Short: "Write one web storage value for the current origin",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"kind": args[0], "key": args[1], "value": args[2]}
			payload, _ := json.Marshal(request)
			response, err := daemonRequest(cmd.Context(), *session, "storage.set", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return err
		},
	}
	return command
}

func newStorageClearCommand(session *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "clear <local|session>",
		Short: "Remove all web storage values of one kind for the current origin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			request := map[string]any{"kind": args[0]}
			payload, _ := json.Marshal(request)
			response, err := daemonRequest(cmd.Context(), *session, "storage.clear", payload)
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelopeFromResponse(cmd, response)
			}
			if !response.Success {
				return responseError(response)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return err
		},
	}
	return command
}

// maxTokensFlag reads the --max-tokens flag value. A missing flag or a
// value <= 0 returns nil (no budget; the daemon returns the full payload).
func maxTokensFlag(cmd *cobra.Command) *int {
	if !cmd.Flags().Changed("max-tokens") {
		return nil
	}
	value, _ := cmd.Flags().GetInt("max-tokens")
	if value <= 0 {
		return nil
	}
	return &value
}

// importCurlCookies parses a curl cookie jar (Netscape format) and sets every
// cookie through the daemon. Lines starting with # are comments; the first
// column is the domain, the sixth the cookie name and the seventh the value.
func importCurlCookies(cmd *cobra.Command, session, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open curl cookie jar: %w", err)
	}
	defer func() { _ = file.Close() }()
	var imported, skipped int
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		cookie, ok := parseCurlCookieLine(scanner.Text())
		if !ok {
			skipped++
			continue
		}
		request := map[string]any{"cookie": cookie, "url": ""}
		payload, _ := json.Marshal(request)
		response, err := daemonRequest(cmd.Context(), session, "cookies.set", payload)
		if err != nil {
			return err
		}
		if !response.Success {
			skipped++
			continue
		}
		imported++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read curl cookie jar: %w", err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "imported %d cookie(s), skipped %d\n", imported, skipped)
	return err
}

// parseCurlCookieLine parses one Netscape cookie-jar line into a Cookie.
// Format: domain \t includeSubdomains \t path \t secure \t expiry \t name \t value.
func parseCurlCookieLine(line string) (engine.Cookie, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return engine.Cookie{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return engine.Cookie{}, false
	}
	expires, _ := strconv.ParseFloat(fields[4], 64)
	return engine.Cookie{
		Name:    fields[5],
		Value:   fields[6],
		Domain:  fields[0],
		Path:    fields[2],
		Expires: expires,
		Secure:  strings.EqualFold(fields[3], "TRUE"),
	}, true
}

func maskSecret(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return "••••"
	}
	return value[:4] + "••••" + value[len(value)-4:]
}

func revealCookie(name, reveal string) bool {
	if reveal == "all" {
		return true
	}
	for _, allowed := range strings.Split(reveal, ",") {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

func cookieFlags(cookie engine.Cookie) string {
	var flags []string
	if cookie.Secure {
		flags = append(flags, "secure")
	}
	if cookie.HTTPOnly {
		flags = append(flags, "httpOnly")
	}
	if cookie.Session {
		flags = append(flags, "session")
	}
	if len(flags) == 0 {
		return "-"
	}
	return strings.Join(flags, ",")
}

type cookieListPayload struct {
	Origin  string          `json:"origin"`
	Cookies []engine.Cookie `json:"cookies"`
}

func cookieListFromResponse(data any) cookieListPayload {
	raw, _ := json.Marshal(data)
	var payload cookieListPayload
	_ = json.Unmarshal(raw, &payload)
	return payload
}

func maskCookiePayload(data any, reveal string) any {
	raw, _ := json.Marshal(data)
	var payload cookieListPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return data
	}
	for i := range payload.Cookies {
		if !revealCookie(payload.Cookies[i].Name, reveal) {
			payload.Cookies[i].Value = maskSecret(payload.Cookies[i].Value)
		}
	}
	return payload
}

type storageListPayload struct {
	Origin string            `json:"origin"`
	Kind   string            `json:"kind"`
	Items  map[string]string `json:"items"`
}

func storageListFromResponse(data any) storageListPayload {
	raw, _ := json.Marshal(data)
	var payload storageListPayload
	_ = json.Unmarshal(raw, &payload)
	if payload.Items == nil {
		payload.Items = map[string]string{}
	}
	return payload
}
