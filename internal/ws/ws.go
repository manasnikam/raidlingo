// Package ws implements the uncompressed RFC 6455 text transport used by Twitch
// and browser clients. Connections have one reader and serialized writers.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type Conn struct {
	net.Conn
	reader     *bufio.Reader
	client     bool
	writeMu    sync.Mutex
	MaxPayload int64
	OnPong     func()
}

func acceptKey(key string) string {
	sum := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(sum[:])
}
func hasToken(value, token string) bool {
	for _, v := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(v), token) {
			return true
		}
	}
	return false
}
func Dial(ctx context.Context, address string) (*Conn, error) {
	return DialWith(ctx, address, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
}
func DialWith(ctx context.Context, address string, dial func(context.Context, string, string) (net.Conn, error)) (*Conn, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil {
		return nil, errors.New("invalid WebSocket URL")
	}
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "wss" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	raw, err := dial(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			raw.Close()
		}
	}()
	transport := raw
	stop := context.AfterFunc(ctx, func() { transport.Close() })
	defer stop()
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	if u.Scheme == "wss" {
		secured := tls.Client(raw, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err = secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		raw = secured
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce)
	req := &http.Request{Method: "GET", URL: u, Host: u.Host, Header: make(http.Header)}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", key)
	if err = req.Write(raw); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(raw)
	res, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 101 || !hasToken(res.Header.Get("Connection"), "upgrade") || !strings.EqualFold(res.Header.Get("Upgrade"), "websocket") || res.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) || res.Header.Get("Sec-WebSocket-Extensions") != "" || res.Header.Get("Sec-WebSocket-Protocol") != "" {
		return nil, fmt.Errorf("WebSocket handshake failed (HTTP %d)", res.StatusCode)
	}
	raw.SetDeadline(time.Time{})
	ok = true
	return &Conn{Conn: raw, reader: reader, client: true, MaxPayload: 1 << 20}, nil
}
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	key := r.Header.Get("Sec-WebSocket-Key")
	decoded, e := base64.StdEncoding.DecodeString(key)
	if r.Method != "GET" || !hasToken(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" || e != nil || len(decoded) != 16 {
		http.Error(w, "Invalid WebSocket handshake", 400)
		return nil, errors.New("invalid WebSocket handshake")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket unavailable", 500)
		return nil, errors.New("hijacking unavailable")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(key))
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{Conn: conn, reader: rw.Reader, MaxPayload: 1 << 20}, nil
}
func (c *Conn) write(op byte, p []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	header := []byte{0x80 | op, 0}
	n := len(p)
	switch {
	case n < 126:
		header[1] = byte(n)
	case n <= 65535:
		header[1] = 126
		header = binary.BigEndian.AppendUint16(header, uint16(n))
	default:
		header[1] = 127
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	if c.client {
		header[1] |= 0x80
		mask := make([]byte, 4)
		if _, err := rand.Read(mask); err != nil {
			return err
		}
		header = append(header, mask...)
		body := append([]byte(nil), p...)
		for i := range body {
			body[i] ^= mask[i%4]
		}
		p = body
	}
	if _, err := io.Copy(c.Conn, strings.NewReader(string(header))); err != nil {
		return err
	}
	_, err := io.Copy(c.Conn, strings.NewReader(string(p)))
	return err
}
func (c *Conn) WriteJSON(v any) error {
	p, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return c.write(1, p)
}
func (c *Conn) WriteText(p []byte) error { return c.write(1, p) }
func (c *Conn) Ping() error              { return c.write(9, nil) }
func (c *Conn) ReadJSON(v any) error {
	p, e := c.ReadText()
	if e != nil {
		return e
	}
	return json.Unmarshal(p, v)
}
func (c *Conn) ReadText() ([]byte, error) {
	var message []byte
	fragmented := false
	for {
		header := make([]byte, 2)
		if _, e := io.ReadFull(c.reader, header); e != nil {
			return nil, e
		}
		fin := header[0]&0x80 != 0
		op := header[0] & 15
		masked := header[1]&0x80 != 0
		if header[0]&0x70 != 0 || masked == c.client {
			return nil, errors.New("invalid WebSocket frame")
		}
		n := uint64(header[1] & 127)
		switch n {
		case 126:
			var b [2]byte
			if _, e := io.ReadFull(c.reader, b[:]); e != nil {
				return nil, e
			}
			n = uint64(binary.BigEndian.Uint16(b[:]))
			if n < 126 {
				return nil, errors.New("noncanonical frame length")
			}
		case 127:
			var b [8]byte
			if _, e := io.ReadFull(c.reader, b[:]); e != nil {
				return nil, e
			}
			n = binary.BigEndian.Uint64(b[:])
			if n < 65536 || n>>63 != 0 {
				return nil, errors.New("invalid frame length")
			}
		}
		if op >= 8 && (!fin || n > 125) {
			return nil, errors.New("invalid control frame")
		}
		limit := c.MaxPayload
		if limit <= 0 {
			limit = 1 << 20
		}
		if n > uint64(limit) || op < 8 && uint64(len(message))+n > uint64(limit) {
			return nil, errors.New("WebSocket payload too large")
		}
		var mask [4]byte
		if masked {
			if _, e := io.ReadFull(c.reader, mask[:]); e != nil {
				return nil, e
			}
		}
		payload := make([]byte, int(n))
		if _, e := io.ReadFull(c.reader, payload); e != nil {
			return nil, e
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		switch op {
		case 8:
			if len(payload) == 1 {
				return nil, errors.New("invalid close frame")
			}
			if len(payload) > 2 && !utf8.Valid(payload[2:]) {
				return nil, errors.New("invalid close reason")
			}
			if len(payload) >= 2 {
				code := binary.BigEndian.Uint16(payload[:2])
				if !(code >= 3000 && code < 5000 || code >= 1000 && code <= 1014 && code != 1004 && code != 1005 && code != 1006) {
					return nil, errors.New("invalid close code")
				}
			}
			_ = c.write(8, payload)
			return nil, io.EOF
		case 9:
			if e := c.write(10, payload); e != nil {
				return nil, e
			}
			continue
		case 10:
			if c.OnPong != nil {
				c.OnPong()
			}
			continue
		case 1:
			if fragmented {
				return nil, errors.New("unexpected data frame")
			}
			message = append(message, payload...)
			fragmented = !fin
		case 0:
			if !fragmented {
				return nil, errors.New("unexpected continuation")
			}
			message = append(message, payload...)
			fragmented = !fin
		default:
			return nil, errors.New("unsupported WebSocket opcode")
		}
		if fin {
			if !utf8.Valid(message) {
				return nil, errors.New("invalid UTF-8")
			}
			return message, nil
		}
	}
}
