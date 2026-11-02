package raidlingo

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"raidlingo/internal/ws"
	"strings"
	"sync"
	"time"
)

var fixtureLines = []map[string]string{
	{"en": "Which game are we playing next?", "es": "¿A qué juego vamos a jugar después?", "hi": "हम अगला कौन सा गेम खेल रहे हैं?"},
	{"en": "Hello from the English chat!", "es": "¡Hola desde el chat en inglés!", "hi": "अंग्रेज़ी चैट से नमस्ते!"},
	{"en": "Hello from the Spanish chat!", "es": "¡Hola desde el chat en español!", "hi": "स्पैनिश चैट से नमस्ते!"},
	{"en": "That was a great round.", "es": "¡Esa partida estuvo increíble!", "hi": "वह बहुत बढ़िया राउंड था।"},
	{"en": "Hello!", "es": "¡Hola!", "hi": "नमस्ते!"},
}

func FixtureTranslation(text string) Bundle {
	for _, line := range fixtureLines {
		for lang, original := range line {
			if strings.EqualFold(NormalizeKey(original), NormalizeKey(text)) {
				return Bundle{lang, cloneMap(line)}
			}
		}
	}
	return Bundle{"en", map[string]string{"en": text, "es": "[fixture ES] " + text, "hi": "[fixture HI] " + text}}
}

type mockSubscription struct {
	spec          Subscription
	session, user string
}
type Simulator struct {
	mu               sync.Mutex
	tokens           map[string]Identity
	apps             map[string]bool
	sockets          map[string]*ws.Conn
	subscriptions    []mockSubscription
	Sent             []map[string]any
	TranslationCalls int
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
}

func NewSimulator() *Simulator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Simulator{tokens: map[string]Identity{}, apps: map[string]bool{}, sockets: map[string]*ws.Conn{}, ctx: ctx, cancel: cancel}
}
func (m *Simulator) Configure(c *Config) {
	base := fmt.Sprintf("http://127.0.0.1:%d/mock", c.Port)
	c.AuthBase = base + "/oauth2"
	c.HelixBase = base + "/helix"
	c.EventSubURL = strings.Replace(base, "http:", "ws:", 1) + "/ws"
	c.OpenAIBase = base + "/v1"
}
func (m *Simulator) issue(user string) Grant {
	login := map[string]string{"100": "streamer_a", "200": "streamer_b", "900": "raidlingo_bot"}[user]
	if login == "" {
		login = "wrong_account"
	}
	scopes := RequiredScopes
	if user == "900" {
		scopes = BotScopes
	}
	access := "test-access-" + randomID()
	m.mu.Lock()
	m.tokens[access] = Identity{"test-client", user, login, scopes, 3600}
	m.mu.Unlock()
	return Grant{access, "refresh-" + user, 3600}
}
func (m *Simulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/oauth2/authorize":
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Local Twitch simulator</title><body style="font:20px sans-serif;background:#eeeae0;color:#282e29;padding:8vw;line-height:1.8"><h1>Local Twitch simulator</h1><p>No Twitch account or password is required. Choose the account for this seat.</p>`)
		for _, user := range []string{"100", "200", "900"} {
			destination, e := url.Parse(q.Get("redirect_uri"))
			if e != nil || destination.Hostname() != "localhost" && destination.Hostname() != "127.0.0.1" {
				errorReply(w, 400, "Invalid mock callback")
				return
			}
			destination.RawQuery = url.Values{"state": {q.Get("state")}, "code": {"code-" + user}}.Encode()
			fmt.Fprintf(w, `<p><a href="%s">Authorize mock %s</a></p>`, html.EscapeString(destination.String()), map[string]string{"100": "streamer_a", "200": "streamer_b", "900": "raidlingo_bot"}[user])
		}
		fmt.Fprint(w, "</body></html>")
	case "/oauth2/token":
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "client_credentials" {
			token := "test-app-" + randomID()
			m.mu.Lock()
			m.apps[token] = true
			m.mu.Unlock()
			jsonReply(w, 200, Grant{AccessToken: token, ExpiresIn: 3600})
			return
		}
		code := r.Form.Get("code")
		prefix := "code-"
		if r.Form.Get("grant_type") == "refresh_token" {
			code = r.Form.Get("refresh_token")
			prefix = "refresh-"
		}
		if !strings.HasPrefix(code, prefix) {
			errorReply(w, 400, "Invalid mock grant")
			return
		}
		jsonReply(w, 200, m.issue(strings.TrimPrefix(code, prefix)))
	case "/oauth2/validate":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "OAuth ")
		m.mu.Lock()
		id, ok := m.tokens[token]
		m.mu.Unlock()
		if !ok {
			errorReply(w, 401, "Invalid access token")
			return
		}
		jsonReply(w, 200, id)
	case "/helix/eventsub/subscriptions":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		m.mu.Lock()
		id, ok := m.tokens[token]
		m.mu.Unlock()
		if !ok {
			errorReply(w, 401, "Invalid access token")
			return
		}
		var body struct {
			Subscription
			Transport struct {
				SessionID string `json:"session_id"`
			} `json:"transport"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body) != nil {
			errorReply(w, 400, "Invalid subscription")
			return
		}
		m.mu.Lock()
		socket := m.sockets[body.Transport.SessionID]
		if socket != nil {
			m.subscriptions = append(m.subscriptions, mockSubscription{body.Subscription, body.Transport.SessionID, id.UserID})
		}
		m.mu.Unlock()
		if socket == nil {
			errorReply(w, 400, "Unknown session")
			return
		}
		jsonReply(w, 202, map[string]any{"data": []any{map[string]string{"id": randomID(), "status": "enabled"}}})
	case "/helix/chat/messages":
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		m.mu.Lock()
		ok := m.apps[token]
		m.mu.Unlock()
		if !ok {
			errorReply(w, 401, "Expected app access token")
			return
		}
		var body struct {
			Broadcaster string `json:"broadcaster_id"`
			Sender      string `json:"sender_id"`
			Message     string `json:"message"`
			Only        bool   `json:"for_source_only"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body) != nil || body.Sender != "900" || !body.Only || utf16Len(body.Message) > 500 {
			errorReply(w, 400, "Invalid relay message")
			return
		}
		id := randomID()
		m.mu.Lock()
		m.Sent = append(m.Sent, map[string]any{"broadcaster_id": body.Broadcaster, "sender_id": body.Sender, "message": body.Message})
		if len(m.Sent) > 100 {
			m.Sent = m.Sent[len(m.Sent)-100:]
		}
		m.mu.Unlock()
		m.Chat(body.Broadcaster, body.Message, "900", "raidlingo_bot", id)
		jsonReply(w, 200, map[string]any{"data": []any{map[string]any{"message_id": id, "is_sent": true}}})
	case "/v1/chat/completions":
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body) != nil {
			errorReply(w, 400, "Invalid request")
			return
		}
		var input struct{ Text string }
		for _, message := range body.Messages {
			if message.Role == "user" {
				_ = json.Unmarshal([]byte(message.Content), &input)
			}
		}
		m.mu.Lock()
		m.TranslationCalls++
		m.mu.Unlock()
		bundle, _ := json.Marshal(FixtureTranslation(input.Text))
		jsonReply(w, 200, map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": string(bundle)}}}})
	case "/ws":
		m.socket(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (m *Simulator) socket(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Upgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	id := randomID()
	m.mu.Lock()
	m.sockets[id] = conn
	source := r.URL.Query().Get("from")
	if source != "" {
		for i := range m.subscriptions {
			if m.subscriptions[i].session == source {
				m.subscriptions[i].session = id
			}
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.sockets, id)
		next := m.subscriptions[:0]
		for _, sub := range m.subscriptions {
			if sub.session != id {
				next = append(next, sub)
			}
		}
		m.subscriptions = next
		m.mu.Unlock()
	}()
	_ = conn.WriteJSON(map[string]any{"metadata": map[string]string{"message_type": "session_welcome", "message_id": randomID()}, "payload": map[string]any{"session": map[string]any{"id": id, "keepalive_timeout_seconds": 60}}})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = conn.ReadText() }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if conn.WriteJSON(map[string]any{"metadata": map[string]string{"message_type": "session_keepalive"}, "payload": map[string]any{}}) != nil {
				return
			}
		}
	}
}
func (m *Simulator) Emit(user string, packet Packet) {
	m.mu.Lock()
	var conn *ws.Conn
	for i := len(m.subscriptions) - 1; i >= 0; i-- {
		sub := m.subscriptions[i]
		if sub.user == user && sub.spec.Type == packet.Payload.Subscription.Type {
			if active := m.sockets[sub.session]; active != nil {
				conn = active
				break
			}
		}
	}
	m.mu.Unlock()
	if conn != nil {
		_ = conn.WriteJSON(packet)
	}
}
func (m *Simulator) Chat(user, text, chatter, name, id string) {
	var p Packet
	p.Metadata.MessageID = id
	p.Metadata.MessageType = "notification"
	p.Metadata.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	p.Payload.Subscription.Type = "channel.chat.message"
	p.Payload.Subscription.Status = "enabled"
	p.Payload.Event = Event{BroadcasterID: user, ChatterID: chatter, ChatterName: name, MessageID: id}
	p.Payload.Event.Message.Text = text
	m.Emit(user, p)
}
func (m *Simulator) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(6 * time.Second)
		defer ticker.Stop()
		sample := 0
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				line := fixtureLines[sample%4]
				sample++
				m.Chat("100", line["en"], "viewer-1", "viewer", randomID())
				m.Chat("200", line["es"], "viewer-2", "viewer", randomID())
				if sample%5 == 0 {
					var p Packet
					p.Metadata.MessageID = randomID()
					p.Metadata.MessageType = "notification"
					p.Payload.Subscription.Type = "channel.raid"
					if sample%10 == 0 {
						p.Payload.Subscription.Type = "channel.subscribe"
					}
					p.Payload.Event = Event{FromName: "raid_friend", UserName: "new_subscriber", Viewers: 42}
					m.Emit("100", p)
				}
			}
		}
	}()
}
func (m *Simulator) Close() {
	m.cancel()
	m.mu.Lock()
	for _, conn := range m.sockets {
		conn.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
