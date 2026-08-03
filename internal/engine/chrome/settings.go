package chrome

import (
	"context"
	"fmt"
	"strings"

	cdproto "github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// SetViewport overrides the device metrics (width, height, scale).
func (e *Engine) SetViewport(ctx context.Context, page engine.Page, width, height int64, scale float64) error {
	if scale <= 0 {
		scale = 1
	}
	params := emulation.SetDeviceMetricsOverride(width, height, scale, false)
	if err := e.call(ctx, page.SessionID, cdproto.CommandEmulationSetDeviceMetricsOverride, params, nil); err != nil {
		return fmt.Errorf("set viewport %dx%d: %w", width, height, err)
	}
	return nil
}

// SetGeo overrides the geolocation (latitude, longitude).
func (e *Engine) SetGeo(ctx context.Context, page engine.Page, latitude, longitude float64) error {
	params := emulation.SetGeolocationOverride().WithLatitude(latitude).WithLongitude(longitude)
	if err := e.call(ctx, page.SessionID, cdproto.CommandEmulationSetGeolocationOverride, params, nil); err != nil {
		return fmt.Errorf("set geolocation: %w", err)
	}
	return nil
}

// SetOffline emulates offline or online network conditions.
func (e *Engine) SetOffline(ctx context.Context, page engine.Page, offline bool) error {
	conditions := []*network.Conditions{{
		URLPattern:         "",
		Latency:            0,
		DownloadThroughput: 0,
		UploadThroughput:   0,
	}}
	if offline {
		conditions[0].DownloadThroughput = -1
		conditions[0].UploadThroughput = -1
	}
	params := network.EmulateNetworkConditionsByRule(conditions)
	if err := e.call(ctx, page.SessionID, cdproto.CommandNetworkEmulateNetworkConditionsByRule, params, nil); err != nil {
		return fmt.Errorf("set offline=%t: %w", offline, err)
	}
	return nil
}

// SetExtraHeaders overrides per-request headers. Authorization and other
// credential headers are rejected unless the credential risk class applies
// (issue B-40: no Authorization header without credential risk class).
func (e *Engine) SetExtraHeaders(ctx context.Context, page engine.Page, headers map[string]string) error {
	for name := range headers {
		if strings.EqualFold(name, "authorization") || strings.EqualFold(name, "proxy-authorization") || strings.EqualFold(name, "cookie") {
			return fmt.Errorf("header %q requires the credential risk class", name)
		}
	}
	raw := make(network.Headers, len(headers))
	for name, value := range headers {
		raw[name] = value
	}
	params := network.SetExtraHTTPHeaders(raw)
	if err := e.call(ctx, page.SessionID, cdproto.CommandNetworkSetExtraHTTPHeaders, params, nil); err != nil {
		return fmt.Errorf("set extra headers: %w", err)
	}
	return nil
}

// SetMedia emulates the prefers-color-scheme media feature.
func (e *Engine) SetMedia(ctx context.Context, page engine.Page, dark bool) error {
	value := "light"
	if dark {
		value = "dark"
	}
	params := emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{
		Name:  "prefers-color-scheme",
		Value: value,
	}})
	if err := e.call(ctx, page.SessionID, cdproto.CommandEmulationSetEmulatedMedia, params, nil); err != nil {
		return fmt.Errorf("set media scheme: %w", err)
	}
	return nil
}

// SetUserAgent overrides the user agent string.
func (e *Engine) SetUserAgent(ctx context.Context, page engine.Page, userAgent string) error {
	params := emulation.SetUserAgentOverride(userAgent)
	if err := e.call(ctx, page.SessionID, cdproto.CommandEmulationSetUserAgentOverride, params, nil); err != nil {
		return fmt.Errorf("set user agent: %w", err)
	}
	return nil
}

// ApplyDevice applies a device descriptor by mapping it to the underlying
// CDP emulation calls. Settings survive navigation because the overrides are
// session-scoped.
func (e *Engine) ApplyDevice(ctx context.Context, page engine.Page, device engine.Device) error {
	if err := e.SetViewport(ctx, page, device.Width, device.Height, device.Scale); err != nil {
		return err
	}
	if device.Mobile {
		params := emulation.SetDeviceMetricsOverride(device.Width, device.Height, device.Scale, true)
		if err := e.call(ctx, page.SessionID, cdproto.CommandEmulationSetDeviceMetricsOverride, params, nil); err != nil {
			return fmt.Errorf("set mobile metrics: %w", err)
		}
	}
	if device.UserAgent != "" {
		if err := e.SetUserAgent(ctx, page, device.UserAgent); err != nil {
			return err
		}
	}
	if device.Touch {
		params := emulation.SetTouchEmulationEnabled(true).WithMaxTouchPoints(5)
		if err := e.call(ctx, page.SessionID, cdproto.CommandEmulationSetTouchEmulationEnabled, params, nil); err != nil {
			return fmt.Errorf("set touch emulation: %w", err)
		}
	}
	return nil
}
