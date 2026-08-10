package proxy

import (
	"bytes"
	"io"
	"net/http"
	"time"
)

// maxCaptureBytes caps how much of a response body we buffer for parsing.
// A response larger than this still streams to the client in full (only
// the capture side is truncated), so a huge completion can't turn tracing
// into an unbounded-memory liability — the exact failure mode reported
// against gateway/logging tools that buffer everything unconditionally.
const maxCaptureBytes = 1 << 20 // 1MB

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
	onComplete func(elapsed time.Duration, statusCode int, body []byte)
}

func (t *teeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.rt.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	resp.Body = &teeReadCloser{
		rc:  resp.Body,
		buf: &bytes.Buffer{},
		onClose: func(captured []byte) {
			t.onComplete(time.Since(start), resp.StatusCode, captured)
		},
	}
	return resp, nil
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
