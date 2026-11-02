package raidlingo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"raidlingo/internal/testutil"
	"raidlingo/internal/ws"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		if check() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func testConfig(t *testing.T) Config {
	t.Helper()
	env := map[string]string{"TWITCH_CLIENT_ID": "test-client", "TWITCH_CLIENT_SECRET": "test-secret", "OPENAI_API_KEY": "test-openai", "TOKEN_STORE_PATH": filepath.Join(t.TempDir(), "tokens.json")}
	c, e := LoadConfig(FindRoot(), func(k string) string { return env[k] }, false, false)
	if e != nil {
		t.Fatal(e)
	}
	c.RetryBase = 5 * time.Millisecond
	c.OutboxGap = 5 * time.Millisecond
	return c
}

type fixture struct {
	session *Session
	app     *App
	sim     *Simulator
	client  *http.Client
	dial    EventDial
	faultMu sync.RWMutex
	fault   func(http.ResponseWriter, *http.Request) bool
}

func newFixture(t *testing.T, configure func(*Config, *TokenStore, *Simulator)) *fixture {
	t.Helper()
	c := testConfig(t)
	sim := NewSimulator()
	sim.Configure(&c)
	store, e := OpenTokenStore(c.TokenPath)
	if e != nil {
		t.Fatal(e)
	}
	if configure != nil {
		configure(&c, store, sim)
	}
	f := &fixture{sim: sim}
	client, dial := testutil.HTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.faultMu.RLock()
		fault := f.fault
		f.faultMu.RUnlock()
		if fault != nil && fault(w, r) {
			return
		}
		f.app.ServeHTTP(w, r)
	}))
	f.client = client
	f.dial = func(ctx context.Context, address string) (EventSocket, error) { return ws.DialWith(ctx, address, dial) }
	f.session = NewSession(c, store, client, nil, f.dial, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.app = NewApp(f.session, sim)
	f.session.Start()
	t.Cleanup(func() { f.session.Close(); sim.Close() })
	return f
}
func (f *fixture) setFault(fn func(http.ResponseWriter, *http.Request) bool) {
	f.faultMu.Lock()
	f.fault = fn
	f.faultMu.Unlock()
}
func (f *fixture) request(path, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://localhost:3000"+path, nil)
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	return w
}
func (f *fixture) start(t *testing.T, slot string) (string, string) {
	t.Helper()
	w := f.request("/auth/twitch?seat="+slot, "")
	if w.Code != 302 {
		t.Fatalf("start OAuth: %d %s", w.Code, w.Body.String())
	}
	u, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	return u.Query().Get("state"), w.Result().Cookies()[0].String()
}
func (f *fixture) authorize(t *testing.T, slot, user string) {
	t.Helper()
	state, cookie := f.start(t, slot)
	w := f.request("/callback?state="+state+"&code=code-"+user, cookie)
	if w.Code != 302 {
		t.Fatalf("authorize %s: %d %s", slot, w.Code, w.Body.String())
	}
}
func (f *fixture) connect(t *testing.T) {
	f.authorize(t, "A", "100")
	f.authorize(t, "B", "200")
	f.authorize(t, "bot", "900")
	eventually(t, func() bool {
		state := f.session.Snapshot()
		return state.Seats["A"].EventSub.Connection == "connected" && state.Seats["B"].EventSub.Connection == "connected"
	})
}
func TestConfigAndEnv(t *testing.T) {
	if _, e := LoadConfig(t.TempDir(), func(string) string { return "" }, false, false); e == nil {
		t.Fatal("missing credentials accepted")
	}
	for _, bad := range []struct{ k, v string }{{"PORT", "0"}, {"LANGUAGE_B", "de"}, {"VOICE_MODE", "invalid"}, {"TWITCH_REDIRECT_URI", "http://localhost/callback?x=1"}, {"CHANNEL_A", "https://twitch.tv/foo"}} {
		t.Run(bad.k, func(t *testing.T) {
			env := map[string]string{"TWITCH_CLIENT_ID": "x", "TWITCH_CLIENT_SECRET": "s", "OPENAI_API_KEY": "k", bad.k: bad.v}
			if _, e := LoadConfig(t.TempDir(), func(k string) string { return env[k] }, false, false); e == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), ".env")
	t.Setenv("RAIDLINGO_TEST_PRESERVE", "existing")
	os.WriteFile(path, []byte("RAIDLINGO_TEST_PRESERVE=file\nRAIDLINGO_TEST_QUOTES=\"hello world\" # comment\n"), 0600)
	t.Cleanup(func() { os.Unsetenv("RAIDLINGO_TEST_QUOTES") })
	if e := LoadEnv(path); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("RAIDLINGO_TEST_PRESERVE") != "existing" || os.Getenv("RAIDLINGO_TEST_QUOTES") != "hello world" {
		t.Fatal("env precedence or quoting failed")
	}
}
func TestOAuthStateAccountsAndPublicSecrets(t *testing.T) {
	f := newFixture(t, nil)
	state, cookie := f.start(t, "A")
	if w := f.request("/callback?state="+state+"&code=code-100", ""); w.Code != 400 {
		t.Fatal("state was not browser bound")
	}
	if w := f.request("/callback?state="+state+"&code=code-100", cookie); w.Code != 302 {
		t.Fatal(w.Body.String())
	}
	if w := f.request("/callback?state="+state+"&code=code-100", cookie); w.Code != 400 {
		t.Fatal("state replay accepted")
	}
	state, cookie = f.start(t, "B")
	if w := f.request("/callback?state="+state+"&code=code-100", cookie); w.Code != 500 || !strings.Contains(w.Body.String(), "different account") {
		t.Fatal("duplicate identity accepted")
	}
	state, cookie = f.start(t, "B")
	f.app.mu.Lock()
	pending := f.app.states[state]
	pending.expires = time.Now().Add(-time.Second)
	f.app.states[state] = pending
	f.app.mu.Unlock()
	if w := f.request("/callback?state="+state+"&code=code-200", cookie); w.Code != 400 {
		t.Fatal("expired state accepted")
	}
	state, cookie = f.start(t, "B")
	if w := f.request("/callback?state="+state+"&error=access_denied", cookie); w.Code != 400 {
		t.Fatal("declined consent accepted")
	}
	r := httptest.NewRequest("GET", "http://attacker.invalid/api/state", nil)
	w := httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("forged host accepted")
	}
	r = httptest.NewRequest("GET", "http://localhost:3000/ws", nil)
	r.Header.Set("Origin", "https://attacker.invalid")
	w = httptest.NewRecorder()
	f.app.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("forged origin accepted")
	}
	body := f.request("/api/state", "").Body.String()
	for _, secret := range []string{"accessToken", "refreshToken", "test-secret", "test-openai", "test-access-"} {
		if strings.Contains(body, secret) {
			t.Fatalf("public state exposes %s", secret)
		}
	}
	info, e := os.Stat(f.session.Config.TokenPath)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("token permissions incorrect")
	}
}
func TestRelayBothDirectionsHindiEchoesAndDedupe(t *testing.T) {
	f := newFixture(t, func(c *Config, _ *TokenStore, _ *Simulator) {
		b := c.Seats["B"]
		b.Language = "hi"
		c.Seats["B"] = b
		c.OverlayLanguage = "hi"
	})
	f.connect(t)
	f.sim.Chat("100", "Hello!", "viewer-1", "viewer", "same-chat")
	f.sim.Chat("100", "Hello!", "viewer-1", "viewer", "same-chat")
	f.sim.Chat("200", "नमस्ते!", "viewer-2", "viewer", "hindi-chat")
	eventually(t, func() bool { return f.session.Snapshot().Relay.Sent == 2 })
	state := f.session.Snapshot()
	if len(state.RecentEvents) != 2 || state.OverlayLanguage != "hi" {
		t.Fatalf("unexpected state: %+v", state)
	}
	eventually(t, func() bool { return f.session.Snapshot().Relay.Ignored >= 2 })
	f.sim.mu.Lock()
	defer f.sim.mu.Unlock()
	if f.sim.Sent[0]["message"] != "🇺🇸 viewer: नमस्ते!" || f.sim.Sent[1]["message"] != "🇮🇳 viewer: Hello!" {
		t.Fatalf("wrong relay posts: %+v", f.sim.Sent)
	}
	if f.sim.Sent[0]["broadcaster_id"] != "200" || f.sim.Sent[1]["broadcaster_id"] != "100" {
		t.Fatal("relay direction incorrect")
	}
}
func TestHistorySharedChatToastsAndTranslationFailure(t *testing.T) {
	f := newFixture(t, nil)
	f.authorize(t, "A", "100")
	f.authorize(t, "B", "200")
	eventually(t, func() bool { return f.session.Snapshot().Seats["B"].EventSub.Connection == "connected" })
	f.sim.Chat("100", "Hello!", "viewer-1", "viewer", "historical")
	eventually(t, func() bool {
		s := f.session.Snapshot()
		return len(s.RecentEvents) == 1 && s.RecentEvents[0].TranslationStatus == "ready"
	})
	f.authorize(t, "bot", "900")
	if f.session.Snapshot().Relay.Sent != 0 {
		t.Fatal("history was replayed")
	}
	f.setFault(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mock/v1/chat/completions" {
			errorReply(w, 401, "invalid key")
			return true
		}
		return false
	})
	f.sim.Chat("100", "Not cached", "viewer", "viewer", "failed")
	eventually(t, func() bool {
		s := f.session.Snapshot()
		return len(s.RecentEvents) == 2 && s.RecentEvents[1].TranslationStatus == "error"
	})
	if !strings.Contains(f.session.Snapshot().RecentEvents[1].TranslationError, "OPENAI_API_KEY") {
		t.Fatal("failure not actionable")
	}
	f.setFault(nil)
	var p Packet
	p.Metadata.MessageID = "mirror"
	p.Metadata.MessageType = "notification"
	p.Payload.Subscription.Type = "channel.chat.message"
	p.Payload.Event = Event{SourceBroadcasterID: "100", MessageID: "historical"}
	p.Payload.Event.Message.Text = "Hello!"
	f.sim.Emit("200", p)
	p.Metadata.MessageID = "raid"
	p.Payload.Subscription.Type = "channel.raid"
	p.Payload.Event = Event{FromName: "raid_friend", Viewers: 42}
	f.sim.Emit("100", p)
	eventually(t, func() bool { return len(f.session.Snapshot().RecentEvents) == 3 })
	toast := f.session.Snapshot().RecentEvents[2]
	if toast.Username != "raid_friend" || len(toast.Translations) != 3 || toast.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatal("toast localization missing")
	}
	f.sim.Chat("100", "Hello!", "viewer", "viewer", "after")
	eventually(t, func() bool { return f.session.Snapshot().Relay.Sent == 1 })
	if !f.session.Snapshot().RecentEvents[3].Cached {
		t.Fatal("cached translation not reused")
	}
}
func TestTokenRefreshRotationAndRestore(t *testing.T) {
	f := newFixture(t, nil)
	grant := f.sim.issue("100")
	token := Token{grant.AccessToken, grant.RefreshToken, 0, "100", "streamer_a", RequiredScopes}
	if e := f.session.Store.Set("A", token); e != nil {
		t.Fatal(e)
	}
	var refreshed atomic.Int64
	f.setFault(func(_ http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mock/oauth2/token" {
			refreshed.Add(1)
		}
		return false
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := f.session.Tokens.Access(context.Background(), "A", false); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if refreshed.Load() != 1 {
		t.Fatalf("refresh was not single-flight: %d", refreshed.Load())
	}
	stored, e := OpenTokenStore(f.session.Config.TokenPath)
	if e != nil {
		t.Fatal(e)
	}
	next, _ := stored.Get("A")
	if next.AccessToken == token.AccessToken {
		t.Fatal("rotation not persisted")
	}
	f.session.Close()
	restored := NewSession(f.session.Config, stored, f.client, nil, f.dial, slog.New(slog.NewTextHandler(io.Discard, nil)))
	restored.Start()
	defer restored.Close()
	eventually(t, func() bool { return restored.Snapshot().Seats["A"].EventSub.Connection == "connected" })
}
func TestValidationRecoversWithoutOAuthAndKeepsRevocationPermanent(t *testing.T) {
	f := newFixture(t, func(c *Config, _ *TokenStore, _ *Simulator) { c.ValidationInterval = 60 * time.Millisecond })
	f.connect(t)
	token, _ := f.session.Store.Get("A")
	var status atomic.Int64
	status.Store(503)
	var requests atomic.Int64
	f.setFault(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mock/oauth2/validate" && r.Header.Get("Authorization") == "OAuth "+token.AccessToken && status.Load() != 0 {
			requests.Add(1)
			errorReply(w, int(status.Load()), "validation failure")
			return true
		}
		return false
	})
	eventually(t, func() bool { return !f.session.Snapshot().Seats["A"].Authorized })
	if !strings.Contains(f.session.Snapshot().Seats["A"].Error, "Retrying automatically") {
		t.Fatal("transient validation failure requires manual auth")
	}
	status.Store(0)
	eventually(t, func() bool { return f.session.Snapshot().Relay.Ready })
	status.Store(403)
	eventually(t, func() bool { return !f.session.Snapshot().Seats["A"].Authorized })
	count := requests.Load()
	time.Sleep(120 * time.Millisecond)
	if requests.Load() != count {
		t.Fatal("permanent authorization failure retried")
	}
}
func TestEventSubRetryHandoffAndTransportRecovery(t *testing.T) {
	f := newFixture(t, nil)
	var fail atomic.Int64
	fail.Store(1)
	var chatCalls atomic.Int64
	f.setFault(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mock/helix/eventsub/subscriptions" {
			data, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(data)))
			if strings.Contains(string(data), "channel.chat.message") {
				chatCalls.Add(1)
				if fail.Add(-1) >= 0 {
					w.Header().Set("Retry-After", "0.02")
					errorReply(w, 429, "temporary rate limit")
					return true
				}
			}
		}
		return false
	})
	f.authorize(t, "A", "100")
	eventually(t, func() bool { return f.session.Snapshot().Seats["A"].EventSub.Connection == "connected" })
	if chatCalls.Load() != 2 {
		t.Fatal("failed chat subscription was not retried")
	}
	original := f.session.Snapshot().Seats["A"].EventSub.SessionID
	f.sim.mu.Lock()
	socket := f.sim.sockets[original]
	f.sim.mu.Unlock()
	socket.WriteJSON(map[string]any{"metadata": map[string]string{"message_type": "session_reconnect"}, "payload": map[string]any{"session": map[string]string{"reconnect_url": f.session.Config.EventSubURL + "?from=" + original}}})
	eventually(t, func() bool {
		status := f.session.Snapshot().Seats["A"].EventSub
		return status.Connection == "connected" && status.SessionID != original
	})
	if chatCalls.Load() != 2 {
		t.Fatal("handoff doubled subscriptions")
	}
	current := f.session.Snapshot().Seats["A"].EventSub.SessionID
	f.sim.mu.Lock()
	socket = f.sim.sockets[current]
	f.sim.mu.Unlock()
	socket.Close()
	eventually(t, func() bool {
		return chatCalls.Load() == 3 && f.session.Snapshot().Seats["A"].EventSub.Connection == "connected"
	})
}
func TestTranslatorConcurrencyKeysLRUAndFailures(t *testing.T) {
	var calls atomic.Int64
	translator := NewTranslator(func(_ context.Context, r TranslationRequest) (Bundle, error) {
		calls.Add(1)
		time.Sleep(time.Millisecond)
		return Bundle{"en", map[string]string{"en": r.Text, "es": "ES " + r.Text, "hi": "HI " + r.Text}}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, e := translator.Translate(context.Background(), TranslationRequest{"  Hello!  ", "auto", "es", []string{"Twitch"}})
			if e != nil || result.Translations["en"] != "  Hello!  " {
				t.Error("translation/original mismatch")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("pending requests were not shared")
	}
	for _, text := range []string{"See https://example.com/ClipA", "See https://example.com/clipa", "Kappa", "kappa", "Ａ", "A"} {
		result, e := translator.Translate(context.Background(), TranslationRequest{text, "auto", "es", nil})
		if e != nil || result.Translated != "ES "+text || result.Cached {
			t.Fatalf("cache collision: %+v %v", result, e)
		}
	}
	translator.capacity = 2
	translator.ttl = 5 * time.Millisecond
	for _, text := range []string{"one", "two", "one", "three"} {
		translator.Translate(context.Background(), TranslationRequest{text, "auto", "es", nil})
	}
	before := calls.Load()
	translator.Translate(context.Background(), TranslationRequest{"two", "auto", "es", nil})
	if calls.Load() != before+1 {
		t.Fatal("LRU eviction failed")
	}
	time.Sleep(8 * time.Millisecond)
	translator.Translate(context.Background(), TranslationRequest{"two", "auto", "es", nil})
	if calls.Load() != before+2 {
		t.Fatal("expiry failed")
	}
	bad := NewTranslator(func(context.Context, TranslationRequest) (Bundle, error) { return Bundle{}, errors.New("offline") })
	for i := 0; i < 2; i++ {
		if _, e := bad.Translate(context.Background(), TranslationRequest{"hello", "auto", "es", nil}); e == nil {
			t.Fatal("provider error ignored")
		}
	}
	if bad.Stats().Requests != 2 || bad.Stats().Entries != 0 {
		t.Fatal("failure was cached")
	}
}
func TestOutboxFinalCooldownAndAmbiguousTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var sent []time.Time
	var labels []string
	outbox := NewOutbox(ctx, time.Millisecond, time.Second, func(_ context.Context, j ChatJob) (string, error) {
		mu.Lock()
		sent = append(sent, time.Now())
		labels = append(labels, j.Message)
		mu.Unlock()
		if j.Message == "limited" {
			return "", &APIError{429, "rate limited", 20 * time.Millisecond}
		}
		if j.Message == "timeout" {
			return "", &TransportError{context.DeadlineExceeded}
		}
		return "id", nil
	})
	done := make(chan struct{})
	go func() { defer close(done); outbox.Run() }()
	defer func() { cancel(); <-done }()
	for _, message := range []string{"limited", "next", "timeout"} {
		outbox.Enqueue(ChatJob{Message: message, Validate: func() error { return nil }, Update: func(Delivery) {}})
	}
	eventually(t, func() bool { return outbox.Stats().Queued == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 5 || sent[3].Sub(sent[2]) < 20*time.Millisecond {
		t.Fatalf("final cooldown ignored: %v %v", labels, sent)
	}
	if outbox.Stats().RateLimited != 3 || outbox.Stats().Failed != 2 || outbox.Stats().Sent != 1 {
		t.Fatal("wrong delivery counters")
	}
}
func TestFormattingAndTranslationProviderRejection(t *testing.T) {
	line := FormatChat("🇮🇳", "viewer", strings.Repeat("🎮 नमस्ते ", 100))
	if utf16Len(line) > 500 || !strings.HasSuffix(line, "…") {
		t.Fatal("Unicode clipping incorrect")
	}
	f := newFixture(t, nil)
	for _, response := range []string{`{"choices":[{"finish_reason":"stop","message":{"refusal":"no"}}]}`, `{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`, `{"choices":[{"finish_reason":"stop","message":{"content":"invalid"}}]}`} {
		f.setFault(func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/mock/v1/chat/completions" {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, response)
				return true
			}
			return false
		})
		if _, e := OpenAIProvider(f.session.Config, f.client)(context.Background(), TranslationRequest{"hello", "auto", "es", nil}); e == nil {
			t.Fatal("provider rejection accepted")
		}
	}
}
func TestStoreDoesNotOverwriteNewLogin(t *testing.T) {
	store, e := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if e != nil {
		t.Fatal(e)
	}
	old := Token{"old", "old-refresh", 0, "1", "first", []string{}}
	newer := Token{"new", "new-refresh", 1000, "2", "second", []string{}}
	store.Set("A", old)
	store.Set("A", newer)
	result, e := store.Replace("A", old, Token{AccessToken: "late-refresh"})
	if e != nil || result.UserID != "2" || result.AccessToken != "new" {
		t.Fatal("refresh overwrote new OAuth login")
	}
}
func TestPublicStateJSONContract(t *testing.T) {
	f := newFixture(t, nil)
	data := f.request("/api/state", "").Body.Bytes()
	var state map[string]any
	if json.Unmarshal(data, &state) != nil {
		t.Fatal("invalid JSON")
	}
	for _, key := range []string{"seats", "bot", "relay", "translation", "recentEvents", "overlayUrl", "overlayLanguage", "voice", "twitchMode"} {
		if _, ok := state[key]; !ok {
			t.Fatalf("missing public field %s", key)
		}
	}
	if events, ok := state["recentEvents"].([]any); !ok || len(events) != 0 {
		t.Fatal("empty events must be []")
	}
}

func TestBrowserStateStreamAndCanonicalOAuthHost(t *testing.T) {
	f := newFixture(t, nil)
	request := httptest.NewRequest("GET", "http://127.0.0.1:3000/auth/twitch?seat=A", nil)
	response := httptest.NewRecorder()
	f.app.ServeHTTP(response, request)
	if response.Code != 302 || response.Header().Get("Location") != "http://localhost:3000/auth/twitch?seat=A" || response.Header().Get("Set-Cookie") != "" {
		t.Fatal("OAuth cookie hostname was not canonicalized")
	}
	conn, err := f.dial(context.Background(), "ws://localhost:3000/ws")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var message struct {
		Type  string      `json:"type"`
		State PublicState `json:"state"`
	}
	if err = conn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "state" || message.State.Phase != 2 || message.State.Seats["A"].Authorized {
		t.Fatal("invalid initial browser state")
	}
	f.authorize(t, "bot", "900")
	for !message.State.Bot.Authorized {
		if err = conn.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
	}
	f.session.Close()
	for {
		if err = conn.ReadJSON(&message); err != nil {
			break
		}
	}
}
func TestRefreshKeepsRotatedCredentialsDuringValidationOutage(t *testing.T) {
	f := newFixture(t, nil)
	grant := f.sim.issue("100")
	old := Token{grant.AccessToken, grant.RefreshToken, 0, "100", "streamer_a", RequiredScopes}
	if err := f.session.Store.Set("A", old); err != nil {
		t.Fatal(err)
	}
	f.setFault(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/mock/oauth2/validate" {
			errorReply(w, 503, "temporary outage")
			return true
		}
		return false
	})
	if _, err := f.session.Tokens.Access(context.Background(), "A", false); !Retryable(err) {
		t.Fatalf("expected retryable validation failure: %v", err)
	}
	rotated, _ := f.session.Store.Get("A")
	if rotated.AccessToken == old.AccessToken || rotated.ExpiresAt != 0 {
		t.Fatal("rotation was lost or unvalidated token became usable")
	}
	f.setFault(nil)
	if _, err := f.session.Tokens.Access(context.Background(), "A", false); err != nil {
		t.Fatal(err)
	}
	valid, _ := f.session.Store.Get("A")
	if valid.ExpiresAt <= time.Now().UnixMilli() {
		t.Fatal("refreshed token did not recover")
	}
}
func TestOutboxStaleMessagesAndChangedAssignments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	outbox := NewOutbox(ctx, 30*time.Millisecond, 10*time.Millisecond, func(context.Context, ChatJob) (string, error) { calls.Add(1); return "id", nil })
	done := make(chan struct{})
	go func() { defer close(done); outbox.Run() }()
	defer func() { cancel(); <-done }()
	update := func(Delivery) { _ = outbox.Stats() } // Callbacks must never run under the counter lock.
	outbox.Enqueue(ChatJob{Validate: func() error { return errors.New("assignment changed") }, Update: update})
	eventually(t, func() bool { return outbox.Stats().Failed == 1 })
	for i := 0; i < 2; i++ {
		outbox.Enqueue(ChatJob{Validate: func() error { return nil }, Update: update})
	}
	eventually(t, func() bool { return outbox.Stats().Queued == 0 })
	if calls.Load() != 1 || outbox.Stats().Dropped != 1 {
		t.Fatal("stale or reassigned message was posted")
	}
}
