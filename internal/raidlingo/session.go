package raidlingo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type translationJob struct {
	activity            *Activity
	at                  time.Time
	source, target, bot string
	hints               []string
}
type Session struct {
	Config           Config
	Store            *TokenStore
	OAuth            *OAuth
	Tokens           *TokenManager
	mu               sync.Mutex
	seats            map[string]SeatStatus
	bot              BotStatus
	events           []*Activity
	seen             map[string]time.Time
	ignored, pending int
	closed           bool
	versions         map[string]int
	streamVersions   map[string]int
	streams          map[string]context.CancelFunc
	authLocks        map[string]*sync.Mutex
	authChanged      map[string]chan struct{}
	retrying         map[string]bool
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	translator       *Translator
	outbox           *Outbox
	jobs             chan translationJob
	changed          chan struct{}
	clientsMu        sync.Mutex
	clients          map[chan []byte]struct{}
	logger           *slog.Logger
	http             HTTPDoer
	dial             EventDial
}

func NewSession(c Config, store *TokenStore, client HTTPDoer, provider TranslationProvider, dial EventDial, logger *slog.Logger) *Session {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	oauth := &OAuth{Config: c, HTTP: client}
	s := &Session{Config: c, Store: store, OAuth: oauth, Tokens: NewTokenManager(store, oauth), ctx: ctx, cancel: cancel, http: client, dial: dial, logger: logger, seats: map[string]SeatStatus{}, seen: map[string]time.Time{}, versions: map[string]int{}, streamVersions: map[string]int{}, streams: map[string]context.CancelFunc{}, authLocks: map[string]*sync.Mutex{}, authChanged: map[string]chan struct{}{}, retrying: map[string]bool{}, jobs: make(chan translationJob, 100), changed: make(chan struct{}, 1), clients: map[chan []byte]struct{}{}}
	for _, slot := range slots {
		s.authLocks[slot] = &sync.Mutex{}
		s.authChanged[slot] = make(chan struct{}, 1)
	}
	for _, slot := range []string{"A", "B"} {
		s.seats[slot] = SeatStatus{Seat: slot, Language: c.Seats[slot].Language, ExpectedChannel: c.Seats[slot].Channel, Scopes: []string{}, EventSub: EventSubStatus{Connection: "disconnected", Subscriptions: map[string]string{}}, Error: "Seat " + slot + " needs Twitch authorization."}
	}
	s.bot = BotStatus{ExpectedChannel: c.BotChannel, Error: "Connect a separate Twitch bot account to post translations."}
	if provider == nil {
		provider = NewTranslationProvider(c, client)
	}
	s.translator = NewTranslator(provider)
	helix := &Helix{Config: c, HTTP: client, Access: oauth.AppAccess}
	s.outbox = NewOutbox(ctx, c.OutboxGap, c.OutboxMaxAge, func(ctx context.Context, j ChatJob) (string, error) {
		return helix.SendChat(ctx, j.BroadcasterID, j.SenderID, j.Message)
	})
	return s
}
func (s *Session) Start() {
	s.wg.Add(6)
	go func() { defer s.wg.Done(); s.outbox.Run() }()
	go func() { defer s.wg.Done(); s.translateLoop() }()
	go func() { defer s.wg.Done(); s.publishLoop() }()
	for _, slot := range slots {
		go func(slot string) { defer s.wg.Done(); s.validationLoop(slot) }(slot)
	}
}
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.clientsMu.Lock()
	for client := range s.clients {
		close(client)
		delete(s.clients, client)
	}
	s.clientsMu.Unlock()
}
func (s *Session) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}
func (s *Session) Snapshot() PublicState {
	// Read worker counters before the session lock: callbacks update session state.
	relay := s.outbox.Stats()
	translation := s.translator.Stats()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := PublicState{Phase: 2, TwitchMode: "live", Seats: map[string]SeatStatus{}, Bot: s.bot, Relay: relay, Translation: translation, RecentEvents: []Activity{}, OverlayURL: s.Config.OverlayURL, OverlayLanguage: s.Config.OverlayLanguage}
	if s.Config.Demo {
		state.TwitchMode = "simulated"
	}
	state.Voice.ConfiguredMode = s.Config.VoiceMode
	for slot, seat := range s.seats {
		seat.Scopes = append([]string{}, seat.Scopes...)
		seat.EventSub.Subscriptions = cloneMap(seat.EventSub.Subscriptions)
		state.Seats[slot] = seat
	}
	state.Relay.Ready = s.bot.Authorized && s.seats["A"].Authorized && s.seats["B"].Authorized
	state.Relay.Ignored = s.ignored
	if !state.Relay.Ready {
		state.Relay.Error = "Connect seat A, seat B, and the bot to relay chat. Unsent history is never replayed."
		for _, retry := range s.retrying {
			if retry {
				state.Relay.Error = "Twitch validation is temporarily unavailable. Retrying automatically; chat relay is paused."
				break
			}
		}
	}
	state.Translation.Model = s.Config.Model
	state.Translation.Provider = s.Config.TranslationProvider
	state.Translation.Pending = s.pending
	for _, event := range s.events {
		copy := *event
		copy.Translations = cloneMap(event.Translations)
		state.RecentEvents = append(state.RecentEvents, copy)
	}
	return state
}
func (s *Session) Subscribe() (<-chan []byte, func()) {
	client := make(chan []byte, 1)
	data, _ := json.Marshal(map[string]any{"type": "state", "state": s.Snapshot()})
	s.clientsMu.Lock()
	if s.ctx.Err() != nil {
		close(client)
		s.clientsMu.Unlock()
		return client, func() {}
	}
	client <- data
	s.clients[client] = struct{}{}
	s.clientsMu.Unlock()
	// A state change during registration must not be lost behind the initial snapshot.
	s.notify()
	return client, func() {
		s.clientsMu.Lock()
		if _, ok := s.clients[client]; ok {
			delete(s.clients, client)
			close(client)
		}
		s.clientsMu.Unlock()
	}
}
func (s *Session) publishLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.changed:
			data, _ := json.Marshal(map[string]any{"type": "state", "state": s.Snapshot()})
			s.clientsMu.Lock()
			for client := range s.clients {
				select {
				case <-client:
				default:
				}
				client <- data
			}
			s.clientsMu.Unlock()
		}
	}
}
func (s *Session) assertChannel(slot string, token Token) error {
	expected := s.Config.BotChannel
	if slot != "bot" {
		expected = s.Config.Seats[slot].Channel
	}
	if expected != "" && expected != token.Login {
		return fmt.Errorf("%s expects @%s, but you authorized @%s; sign into the correct Twitch account", slot, expected, token.Login)
	}
	for _, other := range slots {
		if other != slot {
			existing, ok := s.Store.Get(other)
			if ok && existing.UserID == token.UserID {
				return fmt.Errorf("@%s is already connected to another seat or bot; use a different account", token.Login)
			}
		}
	}
	return nil
}
func (s *Session) Authorize(ctx context.Context, slot, code string) error {
	mu := s.authLocks[slot]
	if mu == nil {
		return errors.New("invalid auth slot")
	}
	mu.Lock()
	defer mu.Unlock()
	grant, err := s.OAuth.Exchange(ctx, code)
	if err != nil {
		return err
	}
	id, err := s.OAuth.Validate(ctx, grant.AccessToken)
	if err != nil {
		return err
	}
	if err = s.OAuth.AssertGrant(id, slot); err != nil {
		return err
	}
	if grant.RefreshToken == "" {
		return errors.New("Twitch did not return a refresh token")
	}
	token := Token{grant.AccessToken, grant.RefreshToken, time.Now().Add(time.Duration(id.ExpiresIn) * time.Second).UnixMilli(), id.UserID, id.Login, id.Scopes}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("server stopped")
	}
	if err = s.assertChannel(slot, token); err != nil {
		return err
	}
	if err = s.Store.Set(slot, token); err != nil {
		return err
	}
	s.versions[slot]++
	s.readyLocked(slot, token, true)
	select {
	case s.authChanged[slot] <- struct{}{}:
	default:
	}
	return nil
}
func (s *Session) readyLocked(slot string, t Token, force bool) {
	s.retrying[slot] = false
	if slot == "bot" {
		s.bot.Authorized = true
		s.bot.Login = t.Login
		s.bot.Error = ""
	} else {
		seat := s.seats[slot]
		restart := force || !seat.Authorized
		seat.Authorized = true
		seat.Login = t.Login
		seat.Scopes = t.Scopes
		seat.Error = ""
		s.seats[slot] = seat
		if restart {
			s.connectLocked(slot, t)
		}
	}
	s.notify()
}
func (s *Session) connectLocked(slot string, t Token) {
	if cancel := s.streams[slot]; cancel != nil {
		cancel()
	}
	s.streamVersions[slot]++
	generation := s.streamVersions[slot]
	ctx, cancel := context.WithCancel(s.ctx)
	s.streams[slot] = cancel
	helix := &Helix{Config: s.Config, HTTP: s.http, Access: func(ctx context.Context, force bool) (string, error) { return s.Tokens.Access(ctx, slot, force) }}
	client := &EventSub{URL: s.Config.EventSubURL, RetryBase: s.Config.RetryBase, Specs: subscriptions(t.UserID), Subscribe: helix.Subscribe, Dial: s.dial, OnStatus: func(status EventSubStatus) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.streamVersions[slot] != generation {
			return
		}
		seat := s.seats[slot]
		seat.EventSub = status
		s.seats[slot] = seat
		s.notify()
	}, OnEvent: func(packet Packet) { s.receive(slot, packet, generation) }}
	s.wg.Add(1)
	go func() { defer s.wg.Done(); client.Run(ctx) }()
}
func (s *Session) validationLoop(slot string) {
	delay := time.Duration(0)
	attempts := 0
	for {
		var timer *time.Timer
		var timeout <-chan time.Time
		if delay >= 0 {
			timer = time.NewTimer(delay)
			timeout = timer.C
		}
		select {
		case <-s.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.authChanged[slot]:
			if timer != nil {
				timer.Stop()
			}
			delay = s.Config.ValidationInterval
			attempts = 0
			continue
		case <-timeout:
		}
		s.mu.Lock()
		version := s.versions[slot]
		s.mu.Unlock()
		if _, ok := s.Store.Get(slot); !ok {
			delay = -1
			continue
		}
		err := s.Tokens.Validate(s.ctx, slot)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		if s.versions[slot] != version {
			s.mu.Unlock()
			delay = s.Config.ValidationInterval
			continue
		}
		token, _ := s.Store.Get(slot)
		if err == nil {
			err = s.assertChannel(slot, token)
		}
		if err == nil {
			s.readyLocked(slot, token, false)
			attempts = 0
			delay = s.Config.ValidationInterval
		} else {
			retry := Retryable(err)
			s.retrying[slot] = retry
			message := err.Error()
			if retry {
				message += " Retrying automatically."
				delay = RetryDelay(err, attempts, s.Config.RetryBase)
				attempts++
			} else {
				delay = -1
			}
			if slot == "bot" {
				s.bot.Authorized = false
				s.bot.Error = message
			} else {
				if cancel := s.streams[slot]; cancel != nil {
					cancel()
				}
				s.streamVersions[slot]++
				seat := s.seats[slot]
				seat.Authorized = false
				seat.Error = message
				seat.EventSub = EventSubStatus{Connection: "error", Subscriptions: map[string]string{}}
				s.seats[slot] = seat
			}
			s.logger.Error("Twitch validation failed", "slot", slot, "error", message)
			s.notify()
		}
		s.mu.Unlock()
	}
}
func (s *Session) Receive(slot string, packet Packet) {
	s.mu.Lock()
	generation := s.streamVersions[slot]
	s.mu.Unlock()
	s.receive(slot, packet, generation)
}
func (s *Session) receive(slot string, p Packet, generation int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.streamVersions[slot] != generation {
		return
	}
	event, kind := p.Payload.Event, p.Payload.Subscription.Type
	if kind != "channel.chat.message" && kind != "channel.raid" && kind != "channel.subscribe" {
		return
	}
	channel, _ := s.Store.Get(slot)
	bot, _ := s.Store.Get("bot")
	chat := kind == "channel.chat.message"
	if chat && (event.SourceBroadcasterID != "" && event.SourceBroadcasterID != channel.UserID || event.ChatterID != "" && event.ChatterID == bot.UserID) {
		s.ignored++
		s.notify()
		return
	}
	id := p.Metadata.MessageID
	if chat && event.MessageID != "" {
		id = event.MessageID
	}
	key := kind + ":" + slot + ":" + id
	if _, exists := s.seen[key]; exists {
		s.ignored++
		s.notify()
		return
	}
	now := time.Now()
	s.seen[key] = now
	for k, at := range s.seen {
		if now.Sub(at) > 10*time.Minute {
			delete(s.seen, k)
		}
	}
	if len(s.seen) > 5000 {
		for k := range s.seen {
			if k != key {
				delete(s.seen, k)
				break
			}
		}
	}
	other := otherSeat(slot)
	username := event.ChatterName
	text := event.Message.Text
	if kind == "channel.raid" {
		username = event.FromName
		text = fmt.Sprintf("%s raided with %d viewers!", username, event.Viewers)
	} else if kind == "channel.subscribe" {
		username = event.UserName
		text = username + " subscribed!"
	}
	if username == "" {
		username = "Twitch"
	}
	activity := &Activity{ID: p.Metadata.MessageID, Seat: slot, Type: kind, Channel: s.seats[slot].Login, Username: username, Text: text, ReceivedAt: now.UTC().Format(time.RFC3339Nano), TargetLanguage: s.seats[other].Language, TranslationStatus: "pending", Delivery: Delivery{Status: "skipped"}}
	s.events = append(s.events, activity)
	if len(s.events) > 100 {
		s.events = s.events[len(s.events)-100:]
	}
	if !chat {
		activity.Translations = map[string]string{"en": text}
		if kind == "channel.raid" {
			activity.Translations["es"] = fmt.Sprintf("¡%s llega con una raid de %d espectadores!", username, event.Viewers)
			activity.Translations["hi"] = fmt.Sprintf("%s ने %d दर्शकों के साथ रेड की!", username, event.Viewers)
		} else {
			activity.Translations["es"] = "¡" + username + " se ha suscrito!"
			activity.Translations["hi"] = username + " ने सब्सक्राइब किया!"
		}
		activity.Translated = activity.Translations[activity.TargetLanguage]
		activity.DetectedLanguage = "en"
		activity.TranslationStatus = "ready"
		activity.ExpiresAt = now.Add(12 * time.Second).UnixMilli()
		s.notify()
		return
	}
	seat := s.seats[slot]
	seat.MessagesReceived++
	s.seats[slot] = seat
	if s.pending >= 100 {
		activity.TranslationStatus = "error"
		activity.TranslationError = "Translation backlog is full; original retained."
		s.notify()
		return
	}
	job := translationJob{activity: activity, at: now, hints: append([]string{}, s.Config.Hints...)}
	if seat.Authorized {
		job.source = channel.UserID
	}
	if s.seats[other].Authorized {
		target, _ := s.Store.Get(other)
		job.target = target.UserID
	}
	if s.bot.Authorized {
		job.bot = bot.UserID
	}
	for _, seat := range s.seats {
		if seat.Login != "" {
			job.hints = append(job.hints, seat.Login)
		}
	}
	s.pending++
	s.jobs <- job
	s.notify()
}
func (s *Session) translateLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case job := <-s.jobs:
			s.translate(job)
		}
	}
}
func (s *Session) translate(job translationJob) {
	a := job.activity
	var result TranslationResult
	var err error
	if time.Since(job.at) > s.Config.TranslationMaxAge {
		err = errors.New("translation became stale in the queue; original retained")
	} else {
		ctx, cancel := context.WithDeadline(s.ctx, job.at.Add(s.Config.TranslationMaxAge))
		result, err = s.translator.Translate(ctx, TranslationRequest{a.Text, "auto", a.TargetLanguage, job.hints})
		cancel()
	}
	s.mu.Lock()
	s.pending--
	if s.closed {
		s.mu.Unlock()
		return
	}
	if err != nil {
		a.TranslationStatus = "error"
		a.TranslationError = err.Error()
		a.Delivery = Delivery{Status: "skipped", Error: "No chat posted because translation failed."}
		s.notify()
		s.mu.Unlock()
		return
	}
	a.Translated = result.Translated
	a.Translations = result.Translations
	a.DetectedLanguage = result.DetectedLanguage
	a.Flag = map[string]string{"en": "🇺🇸", "es": "🇪🇸", "hi": "🇮🇳"}[result.DetectedLanguage]
	if a.Flag == "" {
		a.Flag = "🌐"
	}
	a.Cached = result.Cached
	a.TranslationStatus = "ready"
	if job.source == "" || job.target == "" || job.bot == "" {
		a.Delivery = Delivery{Status: "skipped", Error: "Both seats and the bot must be connected when a message arrives. This line will not be replayed."}
		s.notify()
		s.mu.Unlock()
		return
	}
	message := FormatChat(a.Flag, a.Username, a.Translated)
	s.notify()
	s.mu.Unlock()
	s.outbox.Enqueue(ChatJob{BroadcasterID: job.target, SenderID: job.bot, Message: message, Validate: func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		source, _ := s.Store.Get(a.Seat)
		target, _ := s.Store.Get(otherSeat(a.Seat))
		bot, _ := s.Store.Get("bot")
		if !s.seats[a.Seat].Authorized || !s.seats[otherSeat(a.Seat)].Authorized || !s.bot.Authorized || source.UserID != job.source || target.UserID != job.target || bot.UserID != job.bot {
			return errors.New("a channel or bot connection changed; queued message cancelled")
		}
		return nil
	}, Update: func(d Delivery) { s.mu.Lock(); a.Delivery = d; s.notify(); s.mu.Unlock() }})
}
