package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/budget"
	"github.com/danieljustus/symaira-browse/internal/config"
	fetchcache "github.com/danieljustus/symaira-browse/internal/fetch/cache"
	"github.com/danieljustus/symaira-browse/internal/output"
)

// newCacheCommand manages the truncate-and-store output cache (issue #23,
// B-19): the full payloads behind truncated responses live here until the
// configured TTL expires.
func newCacheCommand() *cobra.Command {
	command := &cobra.Command{
		GroupID: groupIDDebug,
		Use:     "cache",
		Short:   "Inspect the truncate-and-store output cache",
		Args:    cobra.NoArgs,
	}
	command.AddCommand(newCacheGetCommand())
	command.AddCommand(newCacheListCommand())
	command.AddCommand(newCacheClearCommand())
	return command
}

// cacheFromConfig resolves the output cache root and TTL from the effective
// configuration (~/.cache/symbrowse/out, cache_ttl_hours default 24).
func cacheFromConfig() (*budget.Cache, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return budget.NewCache(filepath.Join(cfg.CacheDir, "out"), time.Duration(cfg.CacheTTLHours)*time.Hour), nil
}

func fetchCacheFromConfig() (*fetchcache.Cache, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return fetchcache.New(filepath.Join(cfg.CacheDir, "fetch"), time.Duration(cfg.CacheTTLHours)*time.Hour, 0), nil
}

type cacheListEntry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Bytes     int64     `json:"bytes"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Expired   bool      `json:"expired"`
}

func allCacheEntries() ([]cacheListEntry, error) {
	outputCache, err := cacheFromConfig()
	if err != nil {
		return nil, err
	}
	fetchCache, err := fetchCacheFromConfig()
	if err != nil {
		return nil, err
	}
	entries := make([]cacheListEntry, 0)
	outputEntries, err := outputCache.List()
	if err != nil {
		return nil, err
	}
	for _, entry := range outputEntries {
		entries = append(entries, cacheListEntry{
			ID: entry.ID, Kind: "output", Bytes: entry.Bytes,
			CreatedAt: entry.CreatedAt, ExpiresAt: entry.ExpiresAt, Expired: entry.Expired,
		})
	}
	for _, entry := range fetchCache.Entries() {
		entries = append(entries, cacheListEntry{
			ID: "fetch:" + entry.Key, Kind: "fetch-response", Bytes: entry.Bytes,
			CreatedAt: entry.StoredAt, ExpiresAt: entry.ExpiresAt, Expired: entry.Expired,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	return entries, nil
}

func loadCacheContent(id string) ([]byte, error) {
	if strings.HasPrefix(id, "fetch:") {
		cache, err := fetchCacheFromConfig()
		if err != nil {
			return nil, err
		}
		return cache.LoadKey(strings.TrimPrefix(id, "fetch:"))
	}
	cache, err := cacheFromConfig()
	if err != nil {
		return nil, err
	}
	return cache.Load(id)
}

func clearAllCaches() (int, error) {
	entries, err := allCacheEntries()
	if err != nil {
		return 0, err
	}
	outputCache, err := cacheFromConfig()
	if err != nil {
		return 0, err
	}
	fetchCache, err := fetchCacheFromConfig()
	if err != nil {
		return 0, err
	}
	if err := outputCache.Clear(); err != nil {
		return 0, err
	}
	if err := fetchCache.Clear(); err != nil {
		return 0, err
	}
	remaining, err := allCacheEntries()
	if err != nil {
		return 0, err
	}
	if len(remaining) != 0 {
		return len(entries) - len(remaining), fmt.Errorf("cache clear left %d entr%s", len(remaining), plural(len(remaining)))
	}
	return len(entries), nil
}

func newCacheGetCommand() *cobra.Command {
	var rangeSpec string
	command := &cobra.Command{
		Use:   "get <id>",
		Short: "Print a cached output (optionally one line range)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := loadCacheContent(args[0])
			if err != nil {
				return err
			}
			if rangeSpec == "" {
				if structuredOutput(cmd) {
					return writeEnvelope(cmd, output.OK(map[string]any{"cache_id": args[0], "content": string(content)}, nil))
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(content))
				return err
			}
			start, end, err := parseRange(rangeSpec)
			if err != nil {
				return err
			}
			lines := budget.LineRange(content, start, end)
			if structuredOutput(cmd) {
				return writeEnvelope(cmd, output.OK(map[string]any{
					"cache_id": args[0],
					"range":    rangeSpec,
					"content":  lines,
				}, nil))
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), lines)
			return err
		},
	}
	command.Flags().StringVar(&rangeSpec, "range", "", "1-indexed inclusive line range a-b (e.g. 40-120)")
	return command
}

// parseRange parses "a-b" into its bounds; either side may be empty to mean
// "from the start" / "to the end".
func parseRange(spec string) (int, int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(spec, "-", 2)
	start, end := 0, 0
	var err error
	if parts[0] != "" {
		start, err = strconv.Atoi(parts[0])
		if err != nil || start < 1 {
			return 0, 0, fmt.Errorf("invalid range %q: start must be a positive line number", spec)
		}
	}
	if len(parts) == 2 && parts[1] != "" {
		end, err = strconv.Atoi(parts[1])
		if err != nil || end < start {
			return 0, 0, fmt.Errorf("invalid range %q: end must be >= start", spec)
		}
	}
	return start, end, nil
}

func newCacheListCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "List cache entries with size, age and expiry",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := allCacheEntries()
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelope(cmd, output.OK(map[string]any{"entries": entries}, nil))
			}
			if len(entries) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "cache is empty")
				return err
			}
			for _, entry := range entries {
				age := time.Since(entry.CreatedAt).Truncate(time.Second)
				expiry := "never"
				if !entry.ExpiresAt.IsZero() {
					expiry = time.Until(entry.ExpiresAt).Truncate(time.Second).String() + " left"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s	%s	%d bytes	%s old	%s\n", entry.ID, entry.Kind, entry.Bytes, age, expiry); err != nil {
					return err
				}
			}
			return nil
		},
	}
	return command
}

func newCacheClearCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "clear",
		Short: "Remove all cache entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cleared, err := clearAllCaches()
			if err != nil {
				return err
			}
			if structuredOutput(cmd) {
				return writeEnvelope(cmd, output.OK(map[string]any{"cleared": cleared}, nil))
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "cleared %d cache entr%s\n", cleared, plural(cleared))
			return err
		},
	}
	return command
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
