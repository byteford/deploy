package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestStatusWriter_CapturesExplicitWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	sw.WriteHeader(http.StatusNotFound)
	if sw.status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", sw.status)
	}
}

func TestStatusWriter_ImplicitOKOnWriteWithoutHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	sw.Write([]byte("hello"))
	if sw.status != http.StatusOK {
		t.Errorf("status = %d, want 200 (net/http's own implicit-header behavior)", sw.status)
	}
}

func TestStatusWriter_FirstWriteHeaderWins(t *testing.T) {
	// Matches net/http's own semantics: a second WriteHeader call is a
	// no-op for the status that gets recorded/sent.
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	sw.WriteHeader(http.StatusAccepted)
	sw.WriteHeader(http.StatusInternalServerError)
	if sw.status != http.StatusAccepted {
		t.Errorf("status = %d, want 202 (the first WriteHeader call)", sw.status)
	}
}

// flushRecorder wraps httptest.ResponseRecorder to also satisfy
// http.Flusher and count calls, since ResponseRecorder itself doesn't
// implement Flush.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed int
}

func (f *flushRecorder) Flush() { f.flushed++ }

func TestStatusWriter_FlushForwardsWhenUnderlyingSupportsIt(t *testing.T) {
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	// This is the exact check handleJobStream does — statusWriter must
	// satisfy http.Flusher itself, or SSE streaming breaks entirely once
	// wrapped by the logging middleware.
	flusher, ok := interface{}(sw).(http.Flusher)
	if !ok {
		t.Fatal("statusWriter must implement http.Flusher for SSE streaming to keep working")
	}
	flusher.Flush()
	if rec.flushed != 1 {
		t.Errorf("underlying Flush called %d times, want 1", rec.flushed)
	}
}

func TestStatusWriter_FlushIsNoOpWhenUnderlyingDoesNotSupportIt(t *testing.T) {
	rec := httptest.NewRecorder() // does NOT implement http.Flusher
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}
	sw.Flush() // must not panic
}

func TestLoggingMiddleware_TransparentToResponse(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("hi"))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/whatever", nil)

	loggingMiddleware(inner).ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if rec.Body.String() != "hi" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "hi")
	}
}

func TestLoggingMiddleware_LogsMethodPathAndStatus(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr) // restore the default after the test

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/run", nil)

	loggingMiddleware(inner).ServeHTTP(rec, req)

	logged := buf.String()
	for _, want := range []string{"POST", "/run", "409"} {
		if !strings.Contains(logged, want) {
			t.Errorf("log line missing %q: %s", want, logged)
		}
	}
}

func TestLoggingMiddleware_PreservesFlusherForDownstreamStreaming(t *testing.T) {
	// This is the regression this whole file exists to guard: wrapping
	// a ResponseWriter in the logging middleware must not break a
	// handler's own `w.(http.Flusher)` check further down the chain
	// (handleJobStream depends on exactly this).
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	var sawFlusher bool

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			sawFlusher = true
		}
	})
	loggingMiddleware(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/jobs/x/stream", nil))

	if !sawFlusher {
		t.Fatal("handler behind loggingMiddleware could not type-assert its ResponseWriter to http.Flusher")
	}
}
