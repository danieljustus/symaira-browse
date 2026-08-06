package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-browse/internal/daemon"
)

// Options configures the symbrowse MCP server.
type Options struct {
	// Version is the symbrowse version reported by the server.
	Version string
	// Session is the default session for tool calls without a session
	// argument.
	Session string
	// Executable is the path to the symbrowse binary used to start the
	// daemon. It defaults to os.Args[0].
	Executable string
	// AllowPrivate relaxes the SSRF guard for the daemon this server
	// starts (the --allow-private opt-in).
	AllowPrivate bool
	// Profiles selects the tool profiles to register (issue #31):
	// comma-separated names from core|nav|state|network|debug|flows|all.
	// The empty value registers the default profile (core).
	Profiles string
	// SocketPath resolves the daemon socket path for a session. It
	// defaults to daemon.SocketPath.
	SocketPath func(session string) (string, error)
}

// Server wraps the corekit MCP server with the daemon-proxying tool surface.
type Server struct {
	core       *mcpserver.Server
	options    Options
	verifyOnce sync.Once
}

// New builds the MCP server and registers the tools of the selected
// profiles. An empty Profiles option registers the default core profile.
func New(options Options) (*Server, error) {
	if options.Session == "" {
		options.Session = "default"
	}
	if options.Executable == "" {
		options.Executable = os.Args[0]
	}
	if options.SocketPath == nil {
		options.SocketPath = daemon.SocketPath
	}
	server := &Server{core: mcpserver.New("symbrowse", options.Version), options: options}
	if err := server.RegisterSelection(options.Profiles); err != nil {
		return nil, err
	}
	server.core.SetInstructions(instructions)
	return server, nil
}

// Core returns the underlying corekit server (used by ServeIO tests).
func (s *Server) Core() *mcpserver.Server {
	return s.core
}

// VerifyRunningDaemon warns when a daemon is already running without the
// MCP-mode SSRF policy. Call once at server startup.
func (s *Server) VerifyRunningDaemon() {
	s.verifyRunningDaemon()
}

// instructions is the server-level guidance surfaced to MCP clients as the
// initialize instructions field.
const instructions = `symbrowse drives a real Chrome browser via the local symbrowse daemon.

Every tool accepts an optional "session" argument (default: the server's
default session). Use one session per task; sessions are isolated from each
other.

Security defaults in MCP mode: the domain allowlist and the SSRF guard are
enforced by the daemon. Private/loopback targets are denied unless the server
was started with --allow-private. When a request is blocked, the tool result
carries a warnings[] array describing the denied URLs.

Start with open(url), inspect with snapshot(), act with click/fill/type/press,
and finish with read(url) to get the page as markdown in the symfetch output
schema.`

// proxyTool wraps one table entry into an mcpserver tool. The handler decodes
// the MCP input, forwards the frame to the session daemon, and returns the
// daemon data. When the daemon attached warnings (network policy blocks
// etc.), the result is {"data": ..., "warnings": [...]} so agents can see
// what was denied; otherwise the raw data is returned.
func (s *Server) proxyTool(tool ProxyTool) *mcpserver.Tool {
	inputSchema := map[string]any{}
	for key, value := range tool.Schema {
		inputSchema[key] = value
	}
	properties, _ := inputSchema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties["session"] = map[string]any{
		"type":        "string",
		"description": "session name (default: " + s.options.Session + ")",
	}
	inputSchema["properties"] = properties
	schemaRaw, err := json.Marshal(inputSchema)
	if err != nil {
		panic(fmt.Sprintf("mcp: marshal schema for %s: %v", tool.Name, err))
	}

	return &mcpserver.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: schemaRaw,
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			decoded := map[string]any{}
			if len(input) > 0 && string(input) != "null" {
				if err := json.Unmarshal(input, &decoded); err != nil {
					return nil, fmt.Errorf("invalid arguments for %s: %w", tool.Name, err)
				}
			}
			session := s.options.Session
			if raw, ok := decoded["session"].(string); ok && raw != "" {
				session = raw
			}
			command := tool.Cmd
			if tool.Command != nil {
				command, err = tool.Command(decoded)
				if err != nil {
					return nil, err
				}
			}
			var frameArgs json.RawMessage
			if tool.Args != nil {
				args, err := tool.Args(decoded)
				if err != nil {
					return nil, err
				}
				if args != nil {
					frameArgs, err = json.Marshal(args)
					if err != nil {
						return nil, fmt.Errorf("marshal arguments for %s: %w", tool.Name, err)
					}
				}
			}
			// Output-heavy tools get a stricter default token budget in MCP
			// mode (issue #23, B-19): oversized payloads are truncated with a
			// cache handle instead of flooding the model context. Callers can
			// override the default per call via max_tokens.
			var maxTokens *int
			if mcpBudgetedCommands[command] {
				budget := mcpDefaultMaxTokens
				if raw, ok := decoded["max_tokens"].(float64); ok && raw > 0 {
					budget = int(raw)
				}
				maxTokens = &budget
			}
			slog.Debug("mcp tool call", "tool", tool.Name, "session", session, "command", command)
			client, err := s.client(session)
			if err != nil {
				return nil, err
			}
			response, err := client.Request(ctx, daemon.Frame{
				Cmd:       command,
				Args:      frameArgs,
				Session:   session,
				RequestID: fmt.Sprintf("%d", time.Now().UnixNano()),
				MaxTokens: maxTokens,
			})
			if err != nil {
				return nil, fmt.Errorf("%s: %w", tool.Name, err)
			}
			if !response.Success {
				return nil, daemonToolError(response)
			}
			if tool.Result != nil {
				response.Data, err = tool.Result(response.Data)
				if err != nil {
					return nil, err
				}
			}
			if len(response.Warnings) > 0 {
				return map[string]any{"data": response.Data, "warnings": response.Warnings}, nil
			}
			return response.Data, nil
		},
	}
}

// daemonToolError converts a failed daemon response into an MCP tool error.
func daemonToolError(response daemon.Response) error {
	if response.Error == nil {
		return fmt.Errorf("daemon request failed")
	}
	return fmt.Errorf("%s: %s", response.Error.Code, response.Error.Message)
}

// client builds a daemon client for one session. The autostart hook starts
// the daemon with the SSRF guard enabled (the MCP-mode default) so that
// private targets stay denied unless --allow-private was given.
func (s *Server) client(session string) (*daemon.Client, error) {
	path, err := s.options.SocketPath(session)
	if err != nil {
		return nil, err
	}
	return daemon.NewClient(daemon.ClientOptions{
		SocketPath: path,
		Session:    session,
		StartDaemon: func(ctx context.Context) error {
			return daemon.StartDaemonProcessArgs(ctx, s.options.Executable, s.daemonArgs(session)...)
		},
	}), nil
}

// daemonArgs builds the daemon command line started for one session. The
// SSRF guard is always enabled in MCP mode; --allow-private relaxes it.
func (s *Server) daemonArgs(session string) []string {
	args := []string{"daemon", "--session", session, "--ssrf"}
	if s.options.AllowPrivate {
		args = append(args, "--allow-private")
	}
	return args
}

// verifyRunningDaemon checks whether a daemon is already running for the
// default session and warns when its policy does not match the MCP-mode
// defaults. The autostart hook only starts a daemon when the socket is
// unavailable, so a pre-existing daemon would otherwise silently bypass the
// SSRF default.
func (s *Server) verifyRunningDaemon() {
	s.verifyOnce.Do(func() {
		path, err := s.options.SocketPath(s.options.Session)
		if err != nil {
			return
		}
		client := daemon.NewClient(daemon.ClientOptions{SocketPath: path, Session: s.options.Session})
		response, err := client.RequestWithoutAutostart(context.Background(), daemon.Frame{
			Cmd:       "daemon.status",
			Session:   s.options.Session,
			RequestID: fmt.Sprintf("%d", time.Now().UnixNano()),
		})
		if err != nil || !response.Success {
			// No daemon running yet: the autostart hook will start one
			// with the MCP-mode policy.
			return
		}
		raw, err := json.Marshal(response.Data)
		if err != nil {
			return
		}
		var status struct {
			Policy struct {
				AllowedDomains []string `json:"allowed_domains"`
				SSRFEnabled    bool     `json:"ssrf_enabled"`
				AllowPrivate   bool     `json:"allow_private"`
			} `json:"policy"`
		}
		if err := json.Unmarshal(raw, &status); err != nil {
			return
		}
		if !status.Policy.SSRFEnabled {
			slog.Warn("mcp: a daemon is already running without the SSRF guard; private targets are NOT denied. Stop it and let the MCP server start its own daemon, or pass --allow-private deliberately", "session", s.options.Session)
		}
		if s.options.AllowPrivate && !status.Policy.AllowPrivate {
			slog.Warn("mcp: the running daemon does not allow private targets; use --allow-private on the daemon to match this server's option", "session", s.options.Session)
		}
	})
}
