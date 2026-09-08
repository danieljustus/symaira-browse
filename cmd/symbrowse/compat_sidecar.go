package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/danieljustus/symaira-browse/internal/fetch/fetch"
	"github.com/spf13/cobra"
)

type compatHandshakeWire struct {
	Type      string `json:"type"`
	Protocol  uint32 `json:"protocol,omitempty"`
	Component string `json:"component,omitempty"`
	Oracle    string `json:"oracle,omitempty"`
}

type compatRequestWire struct {
	Type         string      `json:"type"`
	ID           uint64      `json:"id,omitempty"`
	Method       string      `json:"method,omitempty"`
	URL          string      `json:"url,omitempty"`
	Profile      string      `json:"profile,omitempty"`
	Headers      [][2]string `json:"headers,omitempty"`
	Body         string      `json:"body,omitempty"`
	TimeoutMS    uint64      `json:"timeout_ms,omitempty"`
	MaxBodyBytes int         `json:"max_body_bytes,omitempty"`
}

type compatResponseWire struct {
	Type            string           `json:"type"`
	ID              uint64           `json:"id,omitempty"`
	OK              bool             `json:"ok,omitempty"`
	Status          int              `json:"status,omitempty"`
	FinalURL        string           `json:"final_url,omitempty"`
	ResponseHeaders [][2]interface{} `json:"headers,omitempty"`
	Error           *compatError     `json:"error,omitempty"`
	Body            string           `json:"body,omitempty"`
}
type compatError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func newCompatSidecarCommand() *cobra.Command {
	return &cobra.Command{GroupID: groupIDDebug, Use: "compat-sidecar", Short: "Run the pinned Go/AzureTLS compatibility sidecar", Args: cobra.NoArgs, RunE: runCompatSidecar}
}

// runCompatSidecar is the rollback executable boundary. Build it with:
// CGO_ENABLED=0 go build -trimpath -o dist/symbrowse-compat ./cmd/symbrowse
// and launch `dist/symbrowse-compat compat-sidecar` from the Rust daemon.
func runCompatSidecar(cmd *cobra.Command, _ []string) error {
	decoder := json.NewDecoder(cmd.InOrStdin())
	encoder := json.NewEncoder(cmd.OutOrStdout())
	var handshake compatHandshakeWire
	if err := decoder.Decode(&handshake); err != nil {
		return err
	}
	if handshake.Type != "handshake" || handshake.Protocol != 1 {
		return fmt.Errorf("compat_protocol_mismatch")
	}
	if err := encoder.Encode(compatHandshakeWire{Type: "handshake_ack", Protocol: 1, Component: "symbrowse-go", Oracle: "go-azuretls-v0.8.0"}); err != nil {
		return err
	}
	for {
		var wire compatRequestWire
		if err := decoder.Decode(&wire); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if wire.Type != "request" {
			return fmt.Errorf("compat_malformed_request")
		}
		response := compatResponseWire{Type: "response", ID: wire.ID}
		client, err := fetch.New(fetch.ParseProfile(wire.Profile), fetch.WithTimeout(int(wire.TimeoutMS/1000)), fetch.WithMaxBody(wire.MaxBodyBytes/1024/1024))
		if err != nil {
			response.Error = &compatError{Code: "compat_integrity_error", Message: err.Error()}
			_ = encoder.Encode(response)
			continue
		}
		req := fetch.Request{URL: wire.URL, Method: wire.Method, Body: []byte(wire.Body), AllowPrivate: true, MaxBody: int64(wire.MaxBodyBytes)}
		req.Headers = make(map[string]string, len(wire.Headers))
		for _, pair := range wire.Headers {
			if len(pair) == 2 {
				req.Headers[pair[0]] = pair[1]
			}
		}
		result, err := client.Fetch(cmd.Context(), req)
		_ = client.Close()
		if err != nil {
			response.Error = &compatError{Code: "compat_fetch_failed", Message: err.Error(), Retryable: false}
			_ = encoder.Encode(response)
			continue
		}
		response.OK = true
		response.Status = result.StatusCode
		response.FinalURL = result.FinalURL
		response.Body = string(result.Body)
		for key, values := range result.Headers {
			response.ResponseHeaders = append(response.ResponseHeaders, [2]interface{}{key, values})
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
}

// Keep the scanner limit documented for operators even though json.Decoder is used.
var _ = bufio.MaxScanTokenSize
var _ = os.Stderr
