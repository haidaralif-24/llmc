package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// anthropicSSEFixture is a realistic (trimmed) Anthropic streaming
// response: message_start, a couple of text deltas, then message_stop.
const anthropicSSEFixture = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","role":"assistant"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world!"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

func collectTokens(t *testing.T, ch <-chan Token) (text string, gotDone bool, gotErr error) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case tok, ok := <-ch:
			if !ok {
				return text, gotDone, gotErr
			}
			if tok.Err != nil {
				gotErr = tok.Err
			}
			text += tok.Text
			if tok.Done {
				gotDone = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for tokens")
		}
	}
}

func TestAnthropicStream_ParsesTextDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key header = %q, want %q", got, "test-key")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader(anthropicSSEFixture))
	}))
	defer srv.Close()

	p := &Anthropic{Endpoint: srv.URL, APIKey: "test-key"}
	ch, err := p.Stream(context.Background(), "claude-sonnet-4-6", []Message{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	text, gotDone, gotErr := collectTokens(t, ch)
	if gotErr != nil {
		t.Fatalf("unexpected error token: %v", gotErr)
	}
	if !gotDone {
		t.Error("expected a Done token, got none")
	}
	if want := "Hello, world!"; text != want {
		t.Errorf("assembled text = %q, want %q", text, want)
	}
}

func TestAnthropicStream_SurfacesInStreamError(t *testing.T) {
	const errFixture = `event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader(errFixture))
	}))
	defer srv.Close()

	p := &Anthropic{Endpoint: srv.URL, APIKey: "test-key"}
	ch, err := p.Stream(context.Background(), "claude-sonnet-4-6", []Message{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	_, _, gotErr := collectTokens(t, ch)
	if gotErr == nil {
		t.Fatal("expected an error token, got none")
	}
	if !strings.Contains(gotErr.Error(), "Overloaded") {
		t.Errorf("error = %v, want it to mention %q", gotErr, "Overloaded")
	}
}

func TestAnthropicStream_SurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	p := &Anthropic{Endpoint: srv.URL, APIKey: "bad-key"}
	ch, err := p.Stream(context.Background(), "claude-sonnet-4-6", []Message{
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	_, _, gotErr := collectTokens(t, ch)
	if gotErr == nil {
		t.Fatal("expected an error token, got none")
	}
	if !strings.Contains(gotErr.Error(), "invalid x-api-key") {
		t.Errorf("error = %v, want it to mention %q", gotErr, "invalid x-api-key")
	}
}

func TestAnthropicStream_SplitsSystemMessage(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	p := &Anthropic{Endpoint: srv.URL, APIKey: "test-key"}
	ch, err := p.Stream(context.Background(), "claude-sonnet-4-6", []Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectTokens(t, ch)

	if !strings.Contains(captured, `"system":"be terse"`) {
		t.Errorf("request body missing top-level system field: %s", captured)
	}
	if strings.Contains(captured, `"role":"system"`) {
		t.Errorf("request body should not contain a system-role message: %s", captured)
	}
}

func TestAnthropicStream_NoAPIKey(t *testing.T) {
	p := &Anthropic{Endpoint: "https://example.invalid"}
	_, err := p.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error for a missing API key, got nil")
	}
}
