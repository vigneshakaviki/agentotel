package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxCaptureBytes caps how much of a response body we buffer for parsing.
// A response larger than this still streams to the client in full (only
// the capture side is truncated), so a huge completion can't turn tracing
// into an unbounded-memory liability — the exact failure mode reported
// against gateway/logging tools that buffer everything unconditionally.
const maxCaptureBytes = 1 << 20 // 1MB

// sessionHeader lets a caller group calls belonging to one agent run.
// Optional: agentotel's whole premise is that it works with no client-side
// changes, so this is for people who want richer attribution and are
// willing to set one header (or wrap their agent in a script that does).
const sessionHeader = "X-Agentotel-Session"

// capture is everything the proxy learned about one completed call. It is
// a struct rather than a parameter list because tracing dimensions
// (attribution, content type, ...) keep getting added, and threading them
// as positional arguments through the transport gets unreadable fast.
type capture struct {
	Elapsed     time.Duration
	StatusCode  int
	ContentType string
	Agent       string
	SessionID   string
	Body        []byte
}

// teeTransport wraps an http.RoundTripper so the response body is streamed
// to the client as it arrives (no added latency, no broken streaming)
// while a bounded copy is captured in parallel for span extraction once
// the body has been fully read.
//
// This exists instead of httputil.ReverseProxy's ModifyResponse hook
// specifically because ModifyResponse requires the full body up front to
// let you rewrite it — which forces buffering even for SSE/chunked
// responses. See https://github.com/golang/go/issues/27816.
type teeTransport struct {
	rt         http.RoundTripper
	onComplete func(capture)
}

func (t *teeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	// Read request-derived attribution here, while the request is still in
	// scope — the body may not be closed until long after RoundTrip returns.
	c := capture{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Agent:       agentFromUserAgent(req.Header.Get("User-Agent")),
		SessionID:   req.Header.Get(sessionHeader),
	}
	resp.Body = &teeReadCloser{
		rc:  resp.Body,
		buf: &bytes.Buffer{},
		onClose: func(captured []byte) {
			c.Elapsed = time.Since(start)
			c.Body = captured
			t.onComplete(c)
		},
	}
	return resp, nil
}

// agentFromUserAgent reduces a User-Agent to its leading product token, so
// spend groups by tool rather than by version: "claude-cli/1.2 (external,
// cli)" and "claude-cli/1.3 (external, cli)" both become "claude-cli".
// Returns "" when there is nothing useful to record.
func agentFromUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return ""
	}
	if i := strings.IndexAny(ua, " /"); i > 0 {
		return ua[:i]
	}
	return ua
}

// teeReadCloser mirrors reads into an internal buffer (up to
// maxCaptureBytes) as the client consumes the real stream, and fires
// onClose exactly once, with whatever was captured, when the client (or
// ReverseProxy, on its behalf) closes the body.
type teeReadCloser struct {
	rc      io.ReadCloser
	buf     *bytes.Buffer
	onClose func([]byte)
	closed  bool
}

func (t *teeReadCloser) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	if n > 0 && t.buf.Len() < maxCaptureBytes {
		remain := maxCaptureBytes - t.buf.Len()
		if remain > n {
			remain = n
		}
		t.buf.Write(p[:remain])
	}
	return n, err
}

func (t *teeReadCloser) Close() error {
	err := t.rc.Close()
	if !t.closed {
		t.closed = true
		t.onClose(t.buf.Bytes())
	}
	return err
}
