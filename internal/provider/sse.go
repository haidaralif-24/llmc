package provider

import (
	"bufio"
	"io"
	"strings"
)

// sseScanner iterates over Server-Sent Events "data:" payloads from a
// stream, in the order they arrive. Event-name lines (event:), id:,
// retry:, comment lines (starting with ':'), and blank framing lines are
// all skipped — callers only ever see the JSON text that followed "data:".
//
// Shared by both adapters since Anthropic and OpenAI-compatible endpoints
// both speak plain SSE for streaming; only the JSON payload shape differs.
type sseScanner struct {
	scanner *bufio.Scanner
}

func newSSEScanner(r io.Reader) *sseScanner {
	s := bufio.NewScanner(r)
	// Default 64KB max token size is plenty for a single SSE line in
	// practice (one delta's worth of text), but give it headroom rather
	// than fail obscurely on an unusually large chunk.
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &sseScanner{scanner: s}
}

// Next returns the next "data:" payload, or ("", false) at EOF or on a
// read error — call Err() after a false return to tell the two apart.
func (s *sseScanner) Next() (string, bool) {
	for s.scanner.Scan() {
		line := s.scanner.Text()
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		return payload, true
	}
	return "", false
}

func (s *sseScanner) Err() error { return s.scanner.Err() }
