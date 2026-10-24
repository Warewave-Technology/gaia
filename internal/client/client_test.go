package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Warewave-Technology/gaia/internal/server"
)

func TestWatchReadsEventsUntilTheEnd(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "3" {
			t.Errorf("after = %q", r.URL.Query().Get("after"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 4\nevent: log\ndata: {\"seq\":4,\"type\":\"log\",\"stdout\":\"pulumi up\"}\n\n")
		_, _ = fmt.Fprint(w, ": keepalive\n\n")
		_, _ = fmt.Fprint(w, "id: 5\nevent: ansible\ndata: {\"seq\":5,\"type\":\"runner_on_ok\",\"host\":\"u1\",\"task\":\"Motd\",\"changed\":true}\n\n")
		_, _ = fmt.Fprint(w, "event: end\ndata: {\"status\":\"succeeded\"}\n\n")
	}))
	defer hs.Close()
	var got []server.Event
	status, err := New(hs.URL).Watch(context.Background(), "run-x", 3, func(e server.Event) error {
		got = append(got, e)
		return nil
	})
	if err != nil || status != "succeeded" {
		t.Fatalf("status %q, %v", status, err)
	}
	if len(got) != 2 || got[0].Stdout != "pulumi up" || got[1].Task != "Motd" || !got[1].Changed {
		t.Errorf("events = %+v", got)
	}
}

func TestWatchFailsWhenTheStreamBreaks(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "id: 1\nevent: log\ndata: {\"seq\":1,\"type\":\"log\"}\n\n")
	}))
	defer hs.Close()
	if _, err := New(hs.URL).Watch(context.Background(), "run-x", 0, func(server.Event) error { return nil }); err == nil {
		t.Error("no error for a stream that ended early")
	}
}

func TestErrorsCarryTheProblem(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"error":"run has already finished"}`)
	}))
	defer hs.Close()
	err := New(hs.URL).Cancel(context.Background(), "run-x")
	if e, ok := err.(*Error); !ok || e.Status != 409 || e.Message != "run has already finished" {
		t.Errorf("err = %#v", err)
	}
}
