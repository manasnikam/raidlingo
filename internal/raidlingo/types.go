package raidlingo

import "time"

type Language string

func validLanguage(s string) bool { return s == "en" || s == "es" || s == "hi" }

var languages = []string{"en", "es", "hi"}
var slots = []string{"A", "B", "bot"}
var RequiredScopes = []string{"user:read:chat", "user:write:chat", "user:bot", "channel:bot", "moderator:read:chatters", "channel:read:subscriptions"}
var BotScopes = []string{"user:write:chat", "user:bot"}

func scopesFor(slot string) []string {
	if slot == "bot" {
		return BotScopes
	}
	return RequiredScopes
}
func otherSeat(slot string) string {
	if slot == "A" {
		return "B"
	}
	return "A"
}

type EventSubStatus struct {
	Connection    string            `json:"connection"`
	SessionID     string            `json:"sessionId,omitempty"`
	Subscriptions map[string]string `json:"subscriptions"`
	Error         string            `json:"error,omitempty"`
}
type SeatStatus struct {
	Seat             string         `json:"seat"`
	Language         string         `json:"language"`
	ExpectedChannel  string         `json:"expectedChannel,omitempty"`
	Login            string         `json:"login,omitempty"`
	Authorized       bool           `json:"authorized"`
	Scopes           []string       `json:"scopes"`
	EventSub         EventSubStatus `json:"eventsub"`
	MessagesReceived int            `json:"messagesReceived"`
	Error            string         `json:"error,omitempty"`
}
type BotStatus struct {
	Authorized      bool   `json:"authorized"`
	ExpectedChannel string `json:"expectedChannel,omitempty"`
	Login           string `json:"login,omitempty"`
	Error           string `json:"error,omitempty"`
}
type Delivery struct {
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	MessageID string `json:"messageId,omitempty"`
}
type Activity struct {
	ID                string            `json:"id"`
	Seat              string            `json:"seat"`
	Type              string            `json:"type"`
	Channel           string            `json:"channel"`
	Username          string            `json:"username"`
	Text              string            `json:"text"`
	ReceivedAt        string            `json:"receivedAt"`
	TargetLanguage    string            `json:"targetLanguage"`
	DetectedLanguage  string            `json:"detectedLanguage,omitempty"`
	Flag              string            `json:"flag,omitempty"`
	Translated        string            `json:"translated,omitempty"`
	Translations      map[string]string `json:"translations,omitempty"`
	TranslationStatus string            `json:"translationStatus"`
	TranslationError  string            `json:"translationError,omitempty"`
	Cached            bool              `json:"cached,omitempty"`
	Delivery          Delivery          `json:"delivery"`
	ExpiresAt         int64             `json:"expiresAt,omitempty"`
}
type RelayStats struct {
	Ready       bool   `json:"ready"`
	Error       string `json:"error,omitempty"`
	Queued      int    `json:"queued"`
	Sent        int    `json:"sent"`
	Failed      int    `json:"failed"`
	Dropped     int    `json:"dropped"`
	RateLimited int    `json:"rateLimited"`
	Ignored     int    `json:"ignored"`
}
type TranslationStats struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Hits     int    `json:"hits"`
	Misses   int    `json:"misses"`
	Requests int    `json:"requests"`
	Errors   int    `json:"errors"`
	Entries  int    `json:"entries"`
	InFlight int    `json:"inFlight"`
	Pending  int    `json:"pending"`
}
type PublicState struct {
	Phase      int    `json:"phase"`
	TwitchMode string `json:"twitchMode"`
	Voice      struct {
		ConfiguredMode string `json:"configuredMode"`
		Available      bool   `json:"available"`
	} `json:"voice"`
	Seats           map[string]SeatStatus `json:"seats"`
	Bot             BotStatus             `json:"bot"`
	Relay           RelayStats            `json:"relay"`
	Translation     TranslationStats      `json:"translation"`
	RecentEvents    []Activity            `json:"recentEvents"`
	OverlayURL      string                `json:"overlayUrl"`
	OverlayLanguage string                `json:"overlayLanguage"`
}
type Packet struct {
	Metadata struct {
		MessageID   string `json:"message_id"`
		MessageType string `json:"message_type"`
		Timestamp   string `json:"message_timestamp"`
	} `json:"metadata"`
	Payload struct {
		Session *struct {
			ID           string `json:"id"`
			Keepalive    *int   `json:"keepalive_timeout_seconds"`
			ReconnectURL string `json:"reconnect_url"`
		} `json:"session,omitempty"`
		Subscription struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"subscription"`
		Event Event `json:"event"`
	} `json:"payload"`
}
type Event struct {
	BroadcasterID       string `json:"broadcaster_user_id"`
	SourceBroadcasterID string `json:"source_broadcaster_user_id"`
	ChatterID           string `json:"chatter_user_id"`
	ChatterName         string `json:"chatter_user_name"`
	MessageID           string `json:"message_id"`
	Message             struct {
		Text string `json:"text"`
	} `json:"message"`
	FromName string `json:"from_broadcaster_user_name"`
	UserName string `json:"user_name"`
	Viewers  int    `json:"viewers"`
}

func cloneMap[K comparable, V any](input map[K]V) map[K]V {
	out := make(map[K]V, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}
func wait(ctxDone <-chan struct{}, d time.Duration) bool {
	if d <= 0 {
		select {
		case <-ctxDone:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctxDone:
		return false
	case <-timer.C:
		return true
	}
}
