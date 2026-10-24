package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Warewave-Technology/gaia/internal/store"
)

// EventsChannel is the PostgreSQL channel a run's new events are announced
// on; the payload is the run ID. Every API replica listens to it and
// streams the events to its clients (design 12.4).
const EventsChannel = "gaia_run_events"

// Event is one structured event of a run: a line of output or an Ansible
// event.
type Event struct {
	Stage   string
	Type    string // log, or an ansible-runner event
	Host    string // UID
	Task    string
	Changed bool
	Failed  bool
	Stdout  string
	Data    json.RawMessage
	Created time.Time
}

// events collects a run's events and writes them in batches: every flush
// interval or every batch size, whichever comes first, then announces
// them.
type events struct {
	store *store.Store
	run   string

	mu      sync.Mutex
	seq     int
	pending [][]any
	stage   string
	line    bytes.Buffer // an output line not finished yet
	err     error

	stop chan struct{}
	done chan struct{}
}

const (
	eventsBatch = 200
	eventsFlush = 250 * time.Millisecond
)

func newEvents(s *store.Store, run string) *events {
	e := &events{store: s, run: run, stop: make(chan struct{}), done: make(chan struct{})}
	go e.loop()
	return e
}

func (e *events) loop() {
	defer close(e.done)
	t := time.NewTicker(eventsFlush)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			e.flush()
			return
		case <-t.C:
			e.flush()
		}
	}
}

// setStage names the stage that output lines belong to.
func (e *events) setStage(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cutLine()
	e.stage = s
}

// add queues an event.
func (e *events) add(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addLocked(ev)
}

func (e *events) addLocked(ev Event) {
	if ev.Created.IsZero() {
		ev.Created = time.Now()
	}
	if ev.Stage == "" {
		ev.Stage = e.stage
	}
	e.seq++
	var data any
	if len(ev.Data) > 0 {
		data = []byte(ev.Data)
	}
	e.pending = append(e.pending, []any{
		e.run, int32(e.seq), ev.Stage, pgText(ev.Host), ev.Type, pgText(ev.Task), //nolint:gosec // event counts stay far below 2^31
		ev.Changed, ev.Failed, pgText(ev.Stdout), data, pgtype.Timestamptz{Time: ev.Created, Valid: true},
	})
	if len(e.pending) >= eventsBatch {
		go e.flush()
	}
}

// Write makes every output line a log event of the current stage.
func (e *events) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			e.cutLine()
			continue
		}
		if b != '\r' {
			e.line.WriteByte(b)
		}
	}
	return len(p), nil
}

func (e *events) cutLine() {
	if e.line.Len() == 0 {
		return
	}
	e.addLocked(Event{Type: "log", Stdout: e.line.String()})
	e.line.Reset()
}

func (e *events) flush() {
	e.mu.Lock()
	rows := e.pending
	e.pending = nil
	e.mu.Unlock()
	if len(rows) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := e.store.TxFunc(ctx, func(tx pgx.Tx) error {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"run_events"},
			[]string{"run_id", "seq", "stage", "host_uid", "event", "task", "changed", "failed", "stdout", "data", "created_at"},
			pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "select pg_notify($1, $2)", EventsChannel, e.run)
		return err
	})
	if err != nil {
		e.mu.Lock()
		if e.err == nil {
			e.err = err
		}
		e.mu.Unlock()
	}
}

// close writes what is left and returns the first write error.
func (e *events) close() error {
	e.mu.Lock()
	e.cutLine()
	e.mu.Unlock()
	close(e.stop)
	<-e.done
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}
