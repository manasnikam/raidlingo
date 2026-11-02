package ws

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"raidlingo/internal/testutil"
	"strings"
	"testing"
	"time"
)

func TestClientServerRoundTripAndPing(t *testing.T) {
	_, dial := testutil.HTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := Upgrade(w, r)
		if e != nil {
			return
		}
		defer conn.Close()
		var request map[string]string
		if e = conn.ReadJSON(&request); e != nil {
			t.Error(e)
			return
		}
		readDone := make(chan struct{})
		go func() { defer close(readDone); _, _ = conn.ReadText() }()
		defer func() { conn.Close(); <-readDone }()
		if e = conn.Ping(); e != nil {
			t.Error(e)
			return
		}
		if e = conn.WriteJSON(request); e != nil {
			t.Error(e)
		}
	}))
	conn, e := DialWith(context.Background(), "ws://localhost:3000/ws", dial)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if e = conn.WriteJSON(map[string]string{"text": "नमस्ते 🎮"}); e != nil {
		t.Fatal(e)
	}
	var result map[string]string
	if e = conn.ReadJSON(&result); e != nil {
		t.Fatal(e)
	}
	if result["text"] != "नमस्ते 🎮" {
		t.Fatal("Unicode roundtrip failed")
	}
}
func frame(fin bool, op byte, payload []byte, masked bool) []byte {
	first := op
	if fin {
		first |= 128
	}
	out := []byte{first, byte(len(payload))}
	if len(payload) >= 126 {
		out[1] = 126
		out = binary.BigEndian.AppendUint16(out, uint16(len(payload)))
	}
	if masked {
		out[1] |= 128
		mask := []byte{1, 2, 3, 4}
		out = append(out, mask...)
		for i, v := range payload {
			out = append(out, v^mask[i%4])
		}
		return out
	}
	return append(out, payload...)
}
func TestFragmentationAndProtocolLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]byte
		want   string
		bad    bool
	}{
		{"fragmented", [][]byte{frame(false, 1, []byte("hello "), true), frame(true, 0, []byte("world"), true)}, "hello world", false},
		{"unmasked", [][]byte{frame(true, 1, []byte("bad"), false)}, "", true},
		{"invalid UTF8", [][]byte{frame(true, 1, []byte{0xff}, true)}, "", true},
		{"unexpected continuation", [][]byte{frame(true, 0, []byte("bad"), true)}, "", true},
		{"payload limit", [][]byte{frame(true, 1, []byte(strings.Repeat("x", 65)), true)}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			a.SetDeadline(time.Now().Add(time.Second))
			conn := &Conn{Conn: a, reader: bufio.NewReader(a), MaxPayload: 64}
			go func() {
				for _, p := range tc.frames {
					_, _ = b.Write(p)
				}
			}()
			text, e := conn.ReadText()
			if tc.bad && e == nil {
				t.Fatal("invalid frame accepted")
			}
			if !tc.bad && (e != nil || string(text) != tc.want) {
				t.Fatalf("read %q: %v", text, e)
			}
		})
	}
}
func TestUpgradeRejectsInvalidHandshake(t *testing.T) {
	client, _ := testutil.HTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = Upgrade(w, r) }))
	response, e := client.Get("http://localhost:3000/ws")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != 400 {
		t.Fatal("invalid handshake accepted")
	}
}
