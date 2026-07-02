package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const openAISSEFixture = `data: {"choices":[{"delta":{"content":"Hello, "}}]}

data: {"choices":[{"delta":{"content":"world!"}}]}

data: [DONE]

`

func TestOpenAICompatibleStream_ParsesContentDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want %q", got, "Bearer test-key")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader(openAISSEFixture))
	}))
	defer srv.Close()

	p := &OpenAICompatible{BaseURL: srv.URL, APIKey: "test-key", Label: "openai"}
	ch, err := p.Stream(context.Background(), "gpt-4o", []Message{
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

func TestOpenAICompatibleStream_MissingDoneIsStillTerminated(t *testing.T) {
	// Some OpenAI-compatible servers close the stream without ever
	// sending a "[DONE]" sentinel — the adapter should still terminate
	// cleanly with a synthesized Done token instead of hanging forever.
	const noDoneFixture = `data: {"choices":[{"delta":{"content":"hi"}}]}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader(noDoneFixture))
	}))
	defer srv.Close()

	p := &OpenAICompatible{BaseURL: srv.URL, Label: "ollama"}
	ch, err := p.Stream(context.Background(), "llama3", []Message{
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
		t.Error("expected a synthesized Done token when the stream closes without [DONE]")
	}
	if text != "hi" {
		t.Errorf("text = %q, want %q", text, "hi")
	}
}

func TestOpenAICompatibleStream_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	// Local servers like Ollama typically need no key at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header = %q, want empty (no key configured)", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := &OpenAICompatible{BaseURL: srv.URL, Label: "ollama"}
	ch, err := p.Stream(context.Background(), "llama3", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectTokens(t, ch)
}

func TestOpenAICompatibleStream_SurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
	}))
	defer srv.Close()

	p := &OpenAICompatible{BaseURL: srv.URL, APIKey: "test-key", Label: "openai"}
	ch, err := p.Stream(context.Background(), "gpt-4o", []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	_, _, gotErr := collectTokens(t, ch)
	if gotErr == nil {
		t.Fatal("expected an error token, got none")
	}
	if !strings.Contains(gotErr.Error(), "rate limit exceeded") {
		t.Errorf("error = %v, want it to mention %q", gotErr, "rate limit exceeded")
	}
}

func TestOpenAICompatibleStream_NoEndpoint(t *testing.T) {
	p := &OpenAICompatible{Label: "openai"}
	_, err := p.Stream(context.Background(), "m", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error for a missing endpoint, got nil")
	}
}
