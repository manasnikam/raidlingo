package raidlingo

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type ChatJob struct {
	BroadcasterID, SenderID, Message string
	Validate                         func() error
	Update                           func(Delivery)
	enqueued                         time.Time
	ready                            chan struct{}
}
type Outbox struct {
	mu          sync.Mutex
	queue       chan ChatJob
	stats       RelayStats
	gap, maxAge time.Duration
	send        func(context.Context, ChatJob) (string, error)
	closed      bool
	ctx         context.Context
}

func NewOutbox(ctx context.Context, gap, maxAge time.Duration, send func(context.Context, ChatJob) (string, error)) *Outbox {
	if gap <= 0 {
		gap = 1600 * time.Millisecond
	}
	if maxAge <= 0 {
		maxAge = 45 * time.Second
	}
	return &Outbox{queue: make(chan ChatJob, 100), gap: gap, maxAge: maxAge, send: send, ctx: ctx}
}
func (o *Outbox) Stats() RelayStats { o.mu.Lock(); defer o.mu.Unlock(); return o.stats }
func (o *Outbox) Enqueue(job ChatJob) {
	o.mu.Lock()
	if o.closed || o.ctx.Err() != nil || len(o.queue) == cap(o.queue) {
		o.stats.Dropped++
		o.mu.Unlock()
		job.Update(Delivery{Status: "dropped", Error: "Chat relay queue is full or stopped."})
		return
	}
	job.enqueued = time.Now()
	job.ready = make(chan struct{})
	o.stats.Queued++
	o.queue <- job
	o.mu.Unlock()
	defer close(job.ready)
	job.Update(Delivery{Status: "queued"})
}
func (o *Outbox) Run() {
	defer func() {
		o.mu.Lock()
		o.closed = true
		o.mu.Unlock()
		for {
			select {
			case job := <-o.queue:
				<-job.ready
				o.finish(job, Delivery{Status: "dropped", Error: "Server stopped before posting."})
			default:
				return
			}
		}
	}()
	var next time.Time
	for {
		select {
		case <-o.ctx.Done():
			return
		case job := <-o.queue:
			<-job.ready
			var outcome Delivery
			for attempt := 0; ; attempt++ {
				if !wait(o.ctx.Done(), time.Until(next)) {
					outcome = Delivery{Status: "dropped", Error: "Server stopped before posting."}
					break
				}
				if time.Since(job.enqueued) > o.maxAge {
					outcome = Delivery{Status: "dropped", Error: "Translation became stale while waiting for Twitch rate limits."}
					break
				}
				if err := job.Validate(); err != nil {
					outcome = Delivery{Status: "error", Error: err.Error()}
					break
				}
				job.Update(Delivery{Status: "sending"})
				next = time.Now().Add(o.gap)
				id, err := o.send(o.ctx, job)
				if err == nil {
					outcome = Delivery{Status: "sent", MessageID: id}
					break
				}
				var api *APIError
				if errors.As(err, &api) && api.Status == 429 {
					retry := api.RetryAfter
					if retry <= 0 {
						retry = 30 * time.Second
					}
					next = maxTime(next, time.Now().Add(retry))
					o.mu.Lock()
					o.stats.RateLimited++
					o.mu.Unlock()
					if attempt < 2 {
						job.Update(Delivery{Status: "rate_limited", Error: "Twitch rate limit reached; waiting before retry."})
						continue
					}
				}
				outcome = Delivery{Status: "error", Error: err.Error()}
				if o.ctx.Err() != nil {
					outcome = Delivery{Status: "dropped", Error: "Server stopped during posting; delivery may be ambiguous."}
				}
				break
			}
			o.finish(job, outcome)
		}
	}
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func (o *Outbox) finish(job ChatJob, d Delivery) {
	o.mu.Lock()
	o.stats.Queued--
	switch d.Status {
	case "sent":
		o.stats.Sent++
	case "error":
		o.stats.Failed++
	case "dropped":
		o.stats.Dropped++
	}
	o.mu.Unlock()
	job.Update(d)
}
func FormatChat(flag, username, text string) string {
	username = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(username)
	runes := []rune(username)
	if len(runes) > 40 {
		username = string(runes[:40])
	}
	prefix := flag + " " + username + ": "
	text = NormalizeKey(text)
	if utf16Len(prefix+text) <= 500 {
		return prefix + text
	}
	var clipped strings.Builder
	remaining := 500 - utf16Len(prefix) - 1
	for _, r := range text {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if remaining < width {
			break
		}
		clipped.WriteRune(r)
		remaining -= width
	}
	return prefix + clipped.String() + "…"
}
