package raidlingo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string { return fmt.Sprintf("Twitch HTTP %d: %s", e.Status, e.Message) }

type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "Twitch request failed: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
func Retryable(err error) bool {
	var netErr *TransportError
	if errors.As(err, &netErr) {
		return true
	}
	var api *APIError
	return errors.As(err, &api) && (api.Status == 408 || api.Status == 429 || api.Status >= 500 && api.Status < 600)
}
func RetryDelay(err error, attempt int, base time.Duration) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	d := base * time.Duration(1<<min(attempt, 20))
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	var api *APIError
	if errors.As(err, &api) && api.RetryAfter > d {
		d = api.RetryAfter
	}
	return d
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func twitchJSON(ctx context.Context, client HTTPDoer, method, address string, headers http.Header, body io.Reader, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, address, body)
	if e != nil {
		return e
	}
	req.Header = headers.Clone()
	res, e := client.Do(req)
	if e != nil {
		return &TransportError{e}
	}
	defer res.Body.Close()
	data, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if e != nil {
		return &TransportError{e}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(data, &payload)
		message := payload.Message
		if message == "" {
			message = payload.Error
		}
		if message == "" {
			message = http.StatusText(res.StatusCode)
		}
		var retry time.Duration
		if seconds, e := strconv.ParseFloat(res.Header.Get("Retry-After"), 64); e == nil {
			retry = time.Duration(seconds * float64(time.Second))
		} else if date, e := http.ParseTime(res.Header.Get("Retry-After")); e == nil {
			retry = time.Until(date)
		}
		if reset, e := strconv.ParseInt(res.Header.Get("Ratelimit-Reset"), 10, 64); e == nil {
			retry = max(retry, time.Until(time.Unix(reset, 0)))
		}
		return &APIError{res.StatusCode, message, max(retry, 0)}
	}
	if out != nil && len(data) > 0 {
		if e = json.Unmarshal(data, out); e != nil {
			return &TransportError{errors.New("invalid Twitch response")}
		}
	}
	return nil
}

type Grant struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}
type Identity struct {
	ClientID  string   `json:"client_id"`
	UserID    string   `json:"user_id"`
	Login     string   `json:"login"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int64    `json:"expires_in"`
}
type OAuth struct {
	Config     Config
	HTTP       HTTPDoer
	appMu      sync.Mutex
	appToken   string
	appExpires time.Time
}

func (o *OAuth) AuthorizeURL(state, slot string) string {
	q := url.Values{"client_id": {o.Config.ClientID}, "redirect_uri": {o.Config.RedirectURI}, "response_type": {"code"}, "scope": {strings.Join(scopesFor(slot), " ")}, "state": {state}, "force_verify": {"true"}}
	return o.Config.AuthBase + "/authorize?" + q.Encode()
}
func (o *OAuth) grant(ctx context.Context, q url.Values) (Grant, error) {
	q.Set("client_id", o.Config.ClientID)
	q.Set("client_secret", o.Config.ClientSecret)
	var grant Grant
	e := twitchJSON(ctx, o.HTTP, "POST", o.Config.AuthBase+"/token", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, strings.NewReader(q.Encode()), &grant)
	if e == nil && (grant.AccessToken == "" || grant.ExpiresIn <= 0) {
		e = errors.New("invalid Twitch token response")
	}
	return grant, e
}
func (o *OAuth) Exchange(ctx context.Context, code string) (Grant, error) {
	return o.grant(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {o.Config.RedirectURI}})
}
func (o *OAuth) Refresh(ctx context.Context, refresh string) (Grant, error) {
	g, e := o.grant(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
	if e == nil && g.RefreshToken == "" {
		e = errors.New("Twitch refresh token missing")
	}
	return g, e
}
func (o *OAuth) Validate(ctx context.Context, access string) (Identity, error) {
	var id Identity
	e := twitchJSON(ctx, o.HTTP, "GET", o.Config.AuthBase+"/validate", http.Header{"Authorization": {"OAuth " + access}}, nil, &id)
	return id, e
}
func (o *OAuth) AssertGrant(id Identity, slot string) error {
	if id.ClientID != o.Config.ClientID || id.UserID == "" || id.Login == "" {
		return errors.New("Twitch token belongs to a different app or is not a user token; reconnect this account")
	}
	for _, scope := range scopesFor(slot) {
		if !slices.Contains(id.Scopes, scope) {
			return fmt.Errorf("missing Twitch scope %s; reconnect and approve all permissions", scope)
		}
	}
	return nil
}
func (o *OAuth) AppAccess(ctx context.Context, force bool) (string, error) {
	o.appMu.Lock()
	defer o.appMu.Unlock()
	if !force && time.Now().Before(o.appExpires) {
		return o.appToken, nil
	}
	grant, e := o.grant(ctx, url.Values{"grant_type": {"client_credentials"}})
	if e != nil {
		return "", e
	}
	o.appToken = grant.AccessToken
	o.appExpires = time.Now().Add(time.Duration(grant.ExpiresIn)*time.Second - time.Minute)
	return o.appToken, nil
}

type TokenManager struct {
	Store *TokenStore
	OAuth *OAuth
	locks map[string]*sync.Mutex
}

func NewTokenManager(s *TokenStore, o *OAuth) *TokenManager {
	m := &TokenManager{Store: s, OAuth: o, locks: map[string]*sync.Mutex{}}
	for _, slot := range slots {
		m.locks[slot] = &sync.Mutex{}
	}
	return m
}
func (m *TokenManager) Access(ctx context.Context, slot string, force bool) (string, error) {
	mu := m.locks[slot]
	if mu == nil {
		return "", errors.New("invalid auth slot")
	}
	mu.Lock()
	defer mu.Unlock()
	token, ok := m.Store.Get(slot)
	if !ok {
		return "", fmt.Errorf("%s needs Twitch authorization", slot)
	}
	if !force && token.ExpiresAt > time.Now().UnixMilli() {
		return token.AccessToken, nil
	}
	grant, e := m.OAuth.Refresh(ctx, token.RefreshToken)
	if e != nil {
		return "", e
	}
	next := token
	next.AccessToken = grant.AccessToken
	next.RefreshToken = grant.RefreshToken
	next.ExpiresAt = 0 // Do not reuse an unvalidated refresh result.
	// Persist token rotation before validation so a transient validation outage cannot lose the new refresh token.
	next, e = m.Store.Replace(slot, token, next)
	if e != nil {
		return "", e
	}
	id, e := m.OAuth.Validate(ctx, next.AccessToken)
	if e != nil {
		return "", e
	}
	if e = m.OAuth.AssertGrant(id, slot); e != nil {
		return "", e
	}
	if id.UserID != next.UserID {
		return "", errors.New("refreshed Twitch identity changed; reconnect this account")
	}
	validated := next
	validated.ExpiresAt = time.Now().Add(time.Duration(id.ExpiresIn) * time.Second).UnixMilli()
	validated.Scopes = id.Scopes
	latest, e := m.Store.Replace(slot, next, validated)
	if e != nil {
		return "", e
	}
	return latest.AccessToken, nil
}
func (m *TokenManager) Validate(ctx context.Context, slot string) error {
	access, e := m.Access(ctx, slot, false)
	if e != nil {
		return e
	}
	id, e := m.OAuth.Validate(ctx, access)
	var api *APIError
	if errors.As(e, &api) && api.Status == 401 {
		access, e = m.Access(ctx, slot, true)
		if e != nil {
			return e
		}
		id, e = m.OAuth.Validate(ctx, access)
	}
	if e != nil {
		return e
	}
	if e = m.OAuth.AssertGrant(id, slot); e != nil {
		return e
	}
	token, _ := m.Store.Get(slot)
	if id.UserID != token.UserID {
		return errors.New("token identity mismatch; reconnect this account")
	}
	return nil
}

type Helix struct {
	Config Config
	HTTP   HTTPDoer
	Access func(context.Context, bool) (string, error)
}

func (h *Helix) Request(ctx context.Context, path string, body, out any) error {
	encoded, e := json.Marshal(body)
	if e != nil {
		return e
	}
	for attempt := 0; attempt < 2; attempt++ {
		access, e := h.Access(ctx, attempt == 1)
		if e != nil {
			return e
		}
		e = twitchJSON(ctx, h.HTTP, "POST", h.Config.HelixBase+path, http.Header{"Client-Id": {h.Config.ClientID}, "Authorization": {"Bearer " + access}, "Content-Type": {"application/json"}}, bytes.NewReader(encoded), out)
		var api *APIError
		if attempt == 0 && errors.As(e, &api) && api.Status == 401 {
			continue
		}
		return e
	}
	return errors.New("Twitch request rejected")
}

type Subscription struct {
	Type      string            `json:"type"`
	Version   string            `json:"version"`
	Condition map[string]string `json:"condition"`
}

func subscriptions(userID string) []Subscription {
	return []Subscription{{"channel.chat.message", "1", map[string]string{"broadcaster_user_id": userID, "user_id": userID}}, {"channel.raid", "1", map[string]string{"to_broadcaster_user_id": userID}}, {"channel.subscribe", "1", map[string]string{"broadcaster_user_id": userID}}}
}
func (h *Helix) Subscribe(ctx context.Context, session string, spec Subscription) error {
	return h.Request(ctx, "/eventsub/subscriptions", struct {
		Subscription
		Transport map[string]string `json:"transport"`
	}{spec, map[string]string{"method": "websocket", "session_id": session}}, nil)
}
func (h *Helix) SendChat(ctx context.Context, broadcaster, sender, message string) (string, error) {
	var res struct {
		Data []struct {
			ID   string                          `json:"message_id"`
			Sent bool                            `json:"is_sent"`
			Drop *struct{ Code, Message string } `json:"drop_reason"`
		} `json:"data"`
	}
	e := h.Request(ctx, "/chat/messages", map[string]any{"broadcaster_id": broadcaster, "sender_id": sender, "message": message, "for_source_only": true}, &res)
	if e != nil {
		return "", e
	}
	if len(res.Data) == 0 {
		return "", errors.New("Twitch did not send the message: empty response")
	}
	first := res.Data[0]
	if !first.Sent || first.ID == "" {
		reason := "unknown drop reason"
		if first.Drop != nil {
			reason = first.Drop.Message
			if reason == "" {
				reason = first.Drop.Code
			}
		}
		return "", fmt.Errorf("Twitch did not send the message: %s", reason)
	}
	return first.ID, nil
}
