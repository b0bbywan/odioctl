package web

import "sync"

// Changes is the broadcaster behind /events: each open stream is a
// Subscriber, told by name what to send again — a section, or the modal of
// a finished action by its id.
type Changes struct {
	mu   sync.Mutex
	subs map[*Subscriber]struct{}
}

func NewChanges() *Changes {
	return &Changes{subs: map[*Subscriber]struct{}{}}
}

// Subscribe registers a stream; Unsubscribe it when it ends.
func (c *Changes) Subscribe() *Subscriber {
	sub := &Subscriber{dirty: map[string]bool{}, wake: make(chan struct{}, 1)}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs[sub] = struct{}{}
	return sub
}

func (c *Changes) Unsubscribe(sub *Subscriber) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.subs, sub)
}

// Changed marks names dirty on every stream.
func (c *Changes) Changed(names ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for sub := range c.subs {
		sub.mark(names)
	}
}

// Subscriber is one /events stream: the names changed since it last sent,
// and a signal that there are some.
type Subscriber struct {
	mu    sync.Mutex
	dirty map[string]bool
	wake  chan struct{}
}

func (sub *Subscriber) mark(names []string) {
	sub.mu.Lock()
	for _, n := range names {
		sub.dirty[n] = true
	}
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default: // already signalled, Take gets everything
	}
}

// Wake fires once something is dirty; Take is what, and clears it.
func (sub *Subscriber) Wake() <-chan struct{} { return sub.wake }

func (sub *Subscriber) Take() []string {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	names := make([]string, 0, len(sub.dirty))
	for n := range sub.dirty {
		names = append(names, n)
	}
	clear(sub.dirty)
	return names
}
