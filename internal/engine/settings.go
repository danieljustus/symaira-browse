package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Device describes one named emulation profile. The device list is data, not
// code (issue B-40): it lives in devices.json and is embedded at build time.
type Device struct {
	Name      string  `json:"name"`
	Width     int64   `json:"width"`
	Height    int64   `json:"height"`
	Scale     float64 `json:"scale"`
	Mobile    bool    `json:"mobile"`
	Touch     bool    `json:"touch"`
	UserAgent string  `json:"user_agent,omitempty"`
}

//go:embed devices.json
var devicesData []byte

// Devices returns the device list sorted by name.
func Devices() ([]Device, error) {
	var devices []Device
	if err := json.Unmarshal(devicesData, &devices); err != nil {
		return nil, fmt.Errorf("decode device list: %w", err)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return devices, nil
}

// DeviceByName resolves a device by exact name.
func DeviceByName(name string) (Device, error) {
	devices, err := Devices()
	if err != nil {
		return Device{}, err
	}
	for _, device := range devices {
		if device.Name == name {
			return device, nil
		}
	}
	return Device{}, fmt.Errorf("unknown device %q", name)
}

// SettingsEngine is the optional engine capability for session-wide
// emulation overrides. Overrides are session-scoped in CDP, so they survive
// navigation within the session.
type SettingsEngine interface {
	SetViewport(context.Context, Page, int64, int64, float64) error
	SetGeo(context.Context, Page, float64, float64) error
	SetOffline(context.Context, Page, bool) error
	SetExtraHeaders(context.Context, Page, map[string]string) error
	SetMedia(context.Context, Page, bool) error
	SetUserAgent(context.Context, Page, string) error
	ApplyDevice(context.Context, Page, Device) error
}

// settingsError wraps a missing settings capability.
func settingsError() error {
	return errors.New("session settings are not supported by this engine")
}

// SetViewport applies a viewport override to the current session.
func (s *NavigationService) SetViewport(ctx context.Context, width, height int64, scale float64) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("viewport dimensions must be positive: %dx%d", width, height)
	}
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.SetViewport(ctx, s.page, width, height, scale)
}

// SetGeo applies a geolocation override.
func (s *NavigationService) SetGeo(ctx context.Context, latitude, longitude float64) error {
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return fmt.Errorf("invalid coordinates: %f, %f", latitude, longitude)
	}
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.SetGeo(ctx, s.page, latitude, longitude)
}

// SetOffline toggles network emulation.
func (s *NavigationService) SetOffline(ctx context.Context, offline bool) error {
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.SetOffline(ctx, s.page, offline)
}

// SetHeaders overrides per-request headers. Authorization, proxy-authorization
// and Cookie headers are rejected: they carry credentials and would require
// the credential risk class (issue B-40).
func (s *NavigationService) SetHeaders(ctx context.Context, headers map[string]string) error {
	for name := range headers {
		if strings.EqualFold(name, "authorization") || strings.EqualFold(name, "proxy-authorization") || strings.EqualFold(name, "cookie") {
			return fmt.Errorf("header %q requires the credential risk class", name)
		}
	}
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.SetExtraHeaders(ctx, s.page, headers)
}

// SetMedia emulates the color scheme.
func (s *NavigationService) SetMedia(ctx context.Context, dark bool) error {
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.SetMedia(ctx, s.page, dark)
}

// SetUserAgent overrides the user agent.
func (s *NavigationService) SetUserAgent(ctx context.Context, userAgent string) error {
	if strings.TrimSpace(userAgent) == "" {
		return errors.New("user agent must not be empty")
	}
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.SetUserAgent(ctx, s.page, userAgent)
}

// SetDevice applies a named device profile.
func (s *NavigationService) SetDevice(ctx context.Context, name string) error {
	device, err := DeviceByName(name)
	if err != nil {
		return err
	}
	settings, ok := s.engine.(SettingsEngine)
	if !ok {
		return settingsError()
	}
	return settings.ApplyDevice(ctx, s.page, device)
}
