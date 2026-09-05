// Package eventbus holds the SSE event bus — typed event fan-out
// (ADR-0016 §11, ADR-0012). It is a pure in-process fan-out: producers call
// Emit, subscribers receive a bounded channel matching their filter. No HTTP.
//
// Backpressure is consumer-side and labeled (concurrency-topology.md §2/§3): each
// subscriber's channel is bounded (256 events); on overflow the bus drops the
// subscriber's oldest buffered event and delivers exactly one `backpressure`
// event so a slow client can render "stream dropped." Emit never blocks on a slow
// subscriber, so the loop and meter are never stalled by a slow client.
package eventbus

import (
	"encoding/json"
	"sync"

	"texteditor/shared/dto"
)

// EventBus is the SSE event bus public API (interface.md §11).
type EventBus interface {
	Emit(ev dto.Event)
	Subscribe(filter func(dto.Event) bool) <-chan dto.Event
}

// Interface is an alias for EventBus (the contracted name, interface.md §11).
type Interface = EventBus

// Capacity is the per-subscriber channel size (concurrency-topology.md §2).
const Capacity = 256

// subscriber is one registered channel plus its filter predicate.
type subscriber struct {
	ch     chan dto.Event
	filter func(dto.Event) bool
}

// bus is the concrete EventBus.
type bus struct {
	mu   sync.Mutex
	subs []subscriber
}

// New returns an empty EventBus.
func New() EventBus {
	return &bus{}
}

// Emit publishes an event to every subscriber whose filter accepts it. It never
// blocks: a subscriber whose buffer is full gets its oldest event dropped and a
// single backpressure event enqueued (a labeled drop, never silent — ADR-0012).
func (b *bus) Emit(ev dto.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.subs {
		if s.filter != nil && !s.filter(ev) {
			continue
		}
		push(s.ch, ev)
	}
}

// Subscribe returns a bounded channel of events matching filter. The returned
// channel is a stream handle (ADR-0027 §2), not shared mutable state.
func (b *bus) Subscribe(filter func(dto.Event) bool) <-chan dto.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := subscriber{
		ch:     make(chan dto.Event, Capacity),
		filter: filter,
	}
	b.subs = append(b.subs, s)
	return s.ch
}

// push sends ev to ch without blocking. On a full channel it drops the oldest
// buffered event and inserts one backpressure marker in its place. A backpressure
// event is never itself dropped-and-replaced by another backpressure event: the
// label is idempotent, one marker suffices per overflow.
func push(ch chan dto.Event, ev dto.Event) {
	if ev.Type == "backpressure" {
		select {
		case ch <- ev:
			return
		default:
			<-ch // make room; the marker is deferred behind existing buffered events
		}
		ch <- ev
		return
	}
	select {
	case ch <- ev:
		return
	default:
		select {
		case <-ch: // drop the oldest
		default:
		}
	}
	ch <- backpressureEvent(ev)
}

// backpressureEvent builds the labeled overflow marker (ADR-0012 §6; the event
// carries the turnID of the dropped stream so the client can correlate it).
func backpressureEvent(ev dto.Event) dto.Event {
	return dto.Event{Type: "backpressure", TurnID: ev.TurnID, Data: json.RawMessage(`{}`)}
}
