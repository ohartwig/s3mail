package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Events is the way from the program to the page.
//
// Until now the page asked every sixty seconds whether anything had happened.
// That is the wrong way round as soon as the program learns of new mail itself:
// it knows, and it should say so. Server-sent events are the smallest thing
// that does it - one direction, plain HTTP, through the same access check as
// everything else. A WebSocket would buy nothing here; nothing travels upwards.
//
// The page keeps its timer as a fallback. A stream that breaks and a browser
// that does not reconnect would otherwise leave the mailbox standing still
// with nobody able to tell.
type events struct {
	mu   sync.Mutex
	subs map[chan string]bool
	done chan struct{}
}

func newEvents() *events {
	return &events{subs: map[chan string]bool{}, done: make(chan struct{})}
}

// send hands one event to every open page.
//
// The channels are buffered and a full one is skipped rather than waited on: a
// page that is not reading must not hold up the mailbox that is trying to tell
// it something. What it misses, the timer catches.
func (e *events) send(name string, data map[string]any) {
	blob, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", name, blob)

	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (e *events) subscribe() chan string {
	ch := make(chan string, 8)
	e.mu.Lock()
	e.subs[ch] = true
	e.mu.Unlock()
	return ch
}

func (e *events) unsubscribe(ch chan string) {
	e.mu.Lock()
	delete(e.subs, ch)
	e.mu.Unlock()
}

// stop releases every open stream. Without it a shutdown would sit and wait for
// connections that are meant to stay open - http.Server.Shutdown waits for
// handlers to return and does not cancel their requests.
func (e *events) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	select {
	case <-e.done:
	default:
		close(e.done)
	}
}

// Notify tells the pages that something arrived in a mailbox.
func (s *Server) Notify(accountID string) {
	s.events.send("mail", map[string]any{"account": accountID})
}

// StopEvents releases the open streams, so a shutdown does not have to wait for
// them.
func (s *Server) StopEvents() { s.events.stop() }

// Subscribe and Unsubscribe let something other than a browser listen in. The
// stream handler is the first such listener; a test is the second, and whatever
// wants to know about new mail next is the third.
func (s *Server) Subscribe() chan string     { return s.events.subscribe() }
func (s *Server) Unsubscribe(ch chan string) { s.events.unsubscribe(ch) }

func (s *Server) eventsRoute() {
	s.mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			s.writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch := s.events.subscribe()
		defer s.events.unsubscribe(ch)

		// A sign of life every half minute. It keeps whatever sits in between
		// from dropping a quiet connection, and it is how this end notices a
		// page that went away without saying goodbye.
		beat := time.NewTicker(30 * time.Second)
		defer beat.Stop()

		for {
			select {
			case msg := <-ch:
				if _, err := fmt.Fprint(w, msg); err != nil {
					return
				}
				flusher.Flush()
			case <-beat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case <-r.Context().Done():
				return
			case <-s.events.done:
				return
			}
		}
	})
}
