package vkcloud

import (
	"context"
	"io"
	"net/http"
	"sync"
)

func (c *Client) acquireRead(ctx context.Context) error {
	c.readOnce.Do(func() {
		count := c.cloud.MaxConcurrentReads
		if count == 0 {
			count = 8
		}
		c.readSlots = make(chan struct{}, count)
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.readSlots <- struct{}{}:
		return nil
	}
}

type readTransport struct {
	base   http.RoundTripper
	client *Client
}

func (t *readTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return t.base.RoundTrip(r)
	}
	if err := t.client.acquireRead(r.Context()); err != nil {
		return nil, err
	}
	release := func() { <-t.client.readSlots }
	response, err := t.base.RoundTrip(r)
	if err != nil {
		release()
		return response, err
	}
	if response.Body == nil {
		release()
	} else {
		response.Body = &readBody{ReadCloser: response.Body, release: release}
	}
	return response, nil
}
func (t *readTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type readBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *readBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}
func (b *readBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.release); return err }
