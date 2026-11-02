package raidlingo

import (
	"context"
	"fmt"
	"net/url"
	"raidlingo/internal/ws"
	"sync"
	"time"
)

type EventSocket interface {
	ReadJSON(any) error
	Close() error
	SetReadDeadline(time.Time) error
}
type EventDial func(context.Context, string) (EventSocket, error)
type EventSub struct {
	URL       string
	RetryBase time.Duration
	Specs     []Subscription
	Subscribe func(context.Context, string, Subscription) error
	Dial      EventDial
	OnStatus  func(EventSubStatus)
	OnEvent   func(Packet)
}
type eventWire struct {
	socket            EventSocket
	packet            Packet
	err               error
	opened, migrating bool
}
type subscriptionWork struct {
	spec     Subscription
	attempts int
	next     time.Time
	inFlight bool
}
type subscriptionResult struct {
	generation    int
	session, kind string
	err           error
}

func (e *EventSub) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	dial := e.Dial
	if dial == nil {
		dial = func(ctx context.Context, u string) (EventSocket, error) { return ws.Dial(ctx, u) }
	}
	base := e.RetryBase
	if base <= 0 {
		base = time.Second
	}
	wire := make(chan eventWire, 128)
	results := make(chan subscriptionResult, 16)
	var workers sync.WaitGroup
	open := func(address string, migrating bool) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			socket, err := dial(ctx, address)
			if err != nil {
				select {
				case wire <- eventWire{err: err, migrating: migrating}:
				case <-ctx.Done():
				}
				return
			}
			defer socket.Close()
			stop := context.AfterFunc(ctx, func() { socket.Close() })
			defer stop()
			select {
			case wire <- eventWire{socket: socket, opened: true, migrating: migrating}:
			case <-ctx.Done():
				return
			}
			keepalive := 15 * time.Second
			for {
				socket.SetReadDeadline(time.Now().Add(keepalive))
				var packet Packet
				err := socket.ReadJSON(&packet)
				if packet.Metadata.MessageType == "session_welcome" && packet.Payload.Session != nil {
					seconds := 10
					if packet.Payload.Session.Keepalive != nil {
						seconds = *packet.Payload.Session.Keepalive
					}
					keepalive = time.Duration(seconds+5) * time.Second
				}
				select {
				case wire <- eventWire{socket: socket, packet: packet, err: err, migrating: migrating}:
				case <-ctx.Done():
					return
				}
				if err != nil {
					return
				}
			}
		}()
	}
	state := EventSubStatus{Connection: "connecting", Subscriptions: map[string]string{}}
	publish := func() { copy := state; copy.Subscriptions = cloneMap(state.Subscriptions); e.OnStatus(copy) }
	health := func() {
		state.Connection = "connected"
		state.Error = ""
		for _, spec := range e.Specs {
			status := state.Subscriptions[spec.Type]
			if status != "enabled" {
				state.Connection = "degraded"
				if status != "" {
					if state.Error != "" {
						state.Error += "; "
					}
					state.Error += spec.Type + ": " + status
				}
			}
		}
		if state.Subscriptions["channel.chat.message"] != "enabled" {
			state.Connection = "error"
		}
		publish()
	}
	var current, candidate EventSocket
	connecting := true
	attempts := 0
	generation := 0
	var reconnectAt time.Time
	work := map[string]*subscriptionWork{}
	seen := map[string]time.Time{}
	subCtx, subCancel := context.WithCancel(ctx)
	defer func() {
		subCancel()
		cancel()
		if current != nil {
			current.Close()
		}
		if candidate != nil {
			candidate.Close()
		}
		workers.Wait()
	}()
	reset := func(reason string) {
		if current != nil {
			current.Close()
		}
		if candidate != nil {
			candidate.Close()
		}
		current = nil
		candidate = nil
		connecting = false
		subCtx, subCancel = replaceSubscriptionContext(ctx, subCancel)
		generation++
		work = map[string]*subscriptionWork{}
		state = EventSubStatus{Connection: "reconnecting", Error: reason, Subscriptions: map[string]string{}}
		reconnectAt = time.Now().Add(RetryDelay(nil, attempts, base))
		attempts++
		publish()
	}
	publish()
	open(e.URL, false)
	ticker := time.NewTicker(min(base, 100*time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-results:
			if result.generation != generation {
				continue
			}
			job := work[result.kind]
			if job == nil {
				continue
			}
			job.inFlight = false
			if result.err == nil {
				state.Subscriptions[result.kind] = "enabled"
				delete(work, result.kind)
			} else if Retryable(result.err) || result.session != state.SessionID {
				state.Subscriptions[result.kind] = result.err.Error() + " Retrying automatically."
				job.next = time.Now().Add(RetryDelay(result.err, job.attempts, base))
				job.attempts++
			} else {
				state.Subscriptions[result.kind] = result.err.Error()
				delete(work, result.kind)
			}
			health()
		case message := <-wire:
			if message.opened {
				if message.migrating {
					candidate = message.socket
				} else {
					current = message.socket
				}
				connecting = false
				continue
			}
			if message.err != nil {
				if message.socket == nil {
					reset("EventSub connection failed: " + message.err.Error())
				} else if message.socket == candidate {
					reset("EventSub handoff failed; restoring subscriptions")
				} else if message.socket == current {
					current = nil
					if candidate == nil && !connecting {
						reset("EventSub closed; reconnecting and restoring subscriptions")
					}
				}
				continue
			}
			packet := message.packet
			switch packet.Metadata.MessageType {
			case "session_welcome":
				if packet.Payload.Session == nil || packet.Payload.Session.ID == "" {
					reset("Twitch welcome is missing its session ID")
					continue
				}
				if message.socket != current && message.socket != candidate {
					continue
				}
				migrating := message.socket == candidate
				if migrating {
					old := current
					current = candidate
					candidate = nil
					if old != nil {
						old.Close()
					}
				} else {
					subCtx, subCancel = replaceSubscriptionContext(ctx, subCancel)
					generation++
					work = map[string]*subscriptionWork{}
					state.Subscriptions = map[string]string{}
					for _, spec := range e.Specs {
						work[spec.Type] = &subscriptionWork{spec: spec}
					}
				}
				state.SessionID = packet.Payload.Session.ID
				state.Error = ""
				attempts = 0
				if migrating {
					health()
				} else {
					state.Connection = "subscribing"
					publish()
				}
			case "notification":
				if message.socket != current && message.socket != candidate {
					continue
				}
				id := packet.Metadata.MessageID
				if _, ok := seen[id]; ok {
					continue
				}
				now := time.Now()
				seen[id] = now
				for key, at := range seen {
					if now.Sub(at) > 10*time.Minute {
						delete(seen, key)
					}
				}
				if len(seen) > 5000 {
					for key := range seen {
						if key != id {
							delete(seen, key)
							break
						}
					}
				}
				e.OnEvent(packet)
			case "session_reconnect":
				if message.socket != current || candidate != nil || connecting {
					continue
				}
				if packet.Payload.Session == nil {
					reset("Twitch reconnect URL missing")
					continue
				}
				target, err := url.Parse(packet.Payload.Session.ReconnectURL)
				origin, _ := url.Parse(e.URL)
				if err != nil || target.Scheme != origin.Scheme || target.Host != origin.Host || target.User != nil {
					reset("Twitch supplied an unexpected EventSub reconnect host")
					continue
				}
				connecting = true
				state.Connection = "reconnecting"
				publish()
				open(target.String(), true)
			case "revocation":
				if message.socket != current {
					continue
				}
				sub := packet.Payload.Subscription
				delete(work, sub.Type)
				state.Subscriptions[sub.Type] = sub.Status
				health()
			}
		case now := <-ticker.C:
			if current == nil && candidate == nil && !connecting && !reconnectAt.IsZero() && !now.Before(reconnectAt) {
				connecting = true
				state.Connection = "connecting"
				publish()
				open(e.URL, false)
			}
			if current == nil || state.SessionID == "" {
				continue
			}
			for kind, job := range work {
				if job.inFlight || now.Before(job.next) {
					continue
				}
				job.inFlight = true
				id, gen, spec, requestCtx := state.SessionID, generation, job.spec, subCtx
				workers.Add(1)
				go func(kind string) {
					defer workers.Done()
					err := e.Subscribe(requestCtx, id, spec)
					select {
					case results <- subscriptionResult{gen, id, kind, err}:
					case <-ctx.Done():
					}
				}(kind)
			}
		}
	}
}
func (e *EventSub) String() string { return fmt.Sprintf("EventSub(%s)", e.URL) }

// Replacing a connection invalidates all outstanding subscription requests.
func replaceSubscriptionContext(parent context.Context, previous context.CancelFunc) (context.Context, context.CancelFunc) {
	previous()
	return context.WithCancel(parent)
}
