package main

import (
	"encoding/json"
	"testing"
)

func TestCompatWireKeepsRequestAndResponseHeadersDistinct(t *testing.T) {
	requestBytes, err := json.Marshal(compatRequestWire{
		Type:    "request",
		ID:      7,
		Headers: [][2]string{{"X-Request", "value"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var request compatRequestWire
	if err := json.Unmarshal(requestBytes, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Headers) != 1 || request.Headers[0] != [2]string{"X-Request", "value"} {
		t.Fatalf("request headers = %#v", request.Headers)
	}

	responseBytes, err := json.Marshal(compatResponseWire{
		Type:            "response",
		ID:              7,
		ResponseHeaders: [][2]interface{}{{"X-Response", []string{"one", "two"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Headers [][]json.RawMessage `json:"headers"`
	}
	if err := json.Unmarshal(responseBytes, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Headers) != 1 || len(response.Headers[0]) != 2 {
		t.Fatalf("response headers = %s", responseBytes)
	}
	var values []string
	if err := json.Unmarshal(response.Headers[0][1], &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0] != "one" || values[1] != "two" {
		t.Fatalf("response values = %#v", values)
	}
}
