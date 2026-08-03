package engine

import (
	"context"
	"strings"
	"testing"
)

// fakeSettingsEngine implements Engine + SettingsEngine for service tests.
type fakeSettingsEngine struct {
	lastCall string
}

func (f *fakeSettingsEngine) Launch(context.Context) error { return nil }
func (f *fakeSettingsEngine) NewContext(context.Context) (Context, error) {
	return Context{ID: "ctx"}, nil
}
func (f *fakeSettingsEngine) NewPage(context.Context, Context, string) (Page, error) {
	return Page{ID: "page"}, nil
}
func (f *fakeSettingsEngine) Navigate(context.Context, Page, string) (NavigationResult, error) {
	return NavigationResult{}, nil
}
func (f *fakeSettingsEngine) Evaluate(context.Context, Page, string) (EvaluationResult, error) {
	return EvaluationResult{}, nil
}
func (f *fakeSettingsEngine) AXTree(context.Context, Page) ([]AXNode, error)   { return nil, nil }
func (f *fakeSettingsEngine) Screenshot(context.Context, Page) ([]byte, error) { return nil, nil }
func (f *fakeSettingsEngine) Close() error                                     { return nil }

func (f *fakeSettingsEngine) SetViewport(context.Context, Page, int64, int64, float64) error {
	f.lastCall = "viewport"
	return nil
}
func (f *fakeSettingsEngine) SetGeo(context.Context, Page, float64, float64) error {
	f.lastCall = "geo"
	return nil
}
func (f *fakeSettingsEngine) SetOffline(context.Context, Page, bool) error {
	f.lastCall = "offline"
	return nil
}
func (f *fakeSettingsEngine) SetExtraHeaders(context.Context, Page, map[string]string) error {
	f.lastCall = "headers"
	return nil
}
func (f *fakeSettingsEngine) SetMedia(context.Context, Page, bool) error {
	f.lastCall = "media"
	return nil
}
func (f *fakeSettingsEngine) SetUserAgent(context.Context, Page, string) error {
	f.lastCall = "user-agent"
	return nil
}
func (f *fakeSettingsEngine) ApplyDevice(context.Context, Page, Device) error {
	f.lastCall = "device"
	return nil
}

func TestDeviceListIsDataDriven(t *testing.T) {
	devices, err := Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) == 0 {
		t.Fatal("device list is empty")
	}
	names := make(map[string]bool, len(devices))
	for _, device := range devices {
		if device.Name == "" || device.Width <= 0 || device.Height <= 0 || device.Scale <= 0 {
			t.Fatalf("invalid device entry: %#v", device)
		}
		if names[device.Name] {
			t.Fatalf("duplicate device name %q", device.Name)
		}
		names[device.Name] = true
	}
	if _, err := DeviceByName(devices[0].Name); err != nil {
		t.Fatalf("DeviceByName(%q): %v", devices[0].Name, err)
	}
	if _, err := DeviceByName("Does Not Exist"); err == nil {
		t.Fatal("unknown device resolved")
	}
}

func TestSetHeadersRejectsCredentialHeaders(t *testing.T) {
	fake := &fakeSettingsEngine{}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	for _, header := range []string{"Authorization", "authorization", "Cookie", "proxy-authorization"} {
		if err := service.SetHeaders(context.Background(), map[string]string{header: "x"}); err == nil {
			t.Fatalf("credential header %q accepted", header)
		}
	}
	if err := service.SetHeaders(context.Background(), map[string]string{"X-Custom": "1"}); err != nil {
		t.Fatalf("custom header rejected: %v", err)
	}
	if !strings.Contains(fake.lastCall, "headers") {
		t.Fatalf("last call = %q", fake.lastCall)
	}
}

func TestSetViewportValidates(t *testing.T) {
	fake := &fakeSettingsEngine{}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	if err := service.SetViewport(context.Background(), 0, 100, 1); err == nil {
		t.Fatal("zero width accepted")
	}
	if err := service.SetViewport(context.Background(), 100, 100, 1); err != nil {
		t.Fatalf("valid viewport rejected: %v", err)
	}
}

func TestSetUserAgentRejectsEmpty(t *testing.T) {
	fake := &fakeSettingsEngine{}
	service := NewNavigationService(fake, Page{ID: "page"}, NavigationOptions{})
	if err := service.SetUserAgent(context.Background(), "  "); err == nil {
		t.Fatal("empty user agent accepted")
	}
	if err := service.SetUserAgent(context.Background(), "Mozilla/5.0 Test"); err != nil {
		t.Fatalf("valid user agent rejected: %v", err)
	}
}
