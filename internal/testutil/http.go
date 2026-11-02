// Package testutil provides real HTTP framing over net.Pipe, without binding ports.
package testutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
)

type address struct{}

func (address) Network() string { return "pipe" }
func (address) String() string  { return "localhost:3000" }

type listener struct {
	connections chan net.Conn
	done        chan struct{}
	once        sync.Once
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.connections:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *listener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *listener) Addr() net.Addr { return address{} }
func HTTP(t testing.TB, handler http.Handler) (*http.Client, func(context.Context, string, string) (net.Conn, error)) {
	t.Helper()
	l := &listener{connections: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: handler}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		err := server.Serve(l)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("HTTP server: %v", err)
		}
	}()
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, peer := net.Pipe()
		select {
		case l.connections <- peer:
			return client, nil
		case <-ctx.Done():
			client.Close()
			peer.Close()
			return nil, ctx.Err()
		case <-l.done:
			client.Close()
			peer.Close()
			return nil, net.ErrClosed
		}
	}
	transport := &http.Transport{DialContext: dial}
	client := &http.Client{Transport: transport}
	t.Cleanup(func() { transport.CloseIdleConnections(); server.Close(); <-finished })
	return client, dial
}
