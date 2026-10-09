// Copyright 2026 The Forgejo Authors.
// SPDX-License-Identifier: GPL-3.0-or-later

package log

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// NewRelicTransport replaces the HTTP transport of the New Relic writers. It is only set by tests.
var NewRelicTransport http.RoundTripper

const (
	newRelicBatchBytes   = 1 << 20
	newRelicMaxBytes     = 16 << 20
	newRelicMaxEvents    = 10000
	newRelicMaxAttempts  = 5
	newRelicFinalTimeout = 1500 * time.Millisecond
)

// NewRelicOption holds the settings of a "newrelic" writer. The key never leaves it: not in JSON (the admin
// configuration page dumps writer options), not in formatted output, not in errors.
type NewRelicOption struct {
	Endpoint string
	Key      string `json:"-"`
}

func (o NewRelicOption) String() string   { return "NewRelicOption{Endpoint:" + o.Endpoint + "}" }
func (o NewRelicOption) GoString() string { return o.String() }

// Validate returns an error that names the setting and never contains the key.
func (o NewRelicOption) Validate() error {
	if o.Key == "" {
		return errors.New("KEY or KEY_URI is required")
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("ENDPOINT must be an https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("ENDPOINT must not contain credentials, a query or a fragment")
	}
	return nil
}

// NewEventWriterNewRelic ships JSON events to the New Relic Log API. Nothing runs until the first event is written.
func NewEventWriterNewRelic(name string, mode WriterMode) EventWriter {
	w := NewEventWriterBase(name, "newrelic", mode)
	// the shipper needs the logger name for its counters: it is the first line of every message
	w.FormatMessage = func(m *WriterMode, e *Event, f string, a ...any) []byte {
		return append([]byte(e.Logger+"\n"), EventFormatJSONMessage(m, e, f, a...)...)
	}
	opt, _ := mode.WriterOption.(NewRelicOption)
	if err := opt.Validate(); err != nil {
		FallbackErrorf("log writer %q is disabled: %v", name, err)
		w.OutputWriteCloser = nopCloser{io.Discard}
		return w
	}
	RegisterSecret(opt.Key)
	w.OutputWriteCloser = newNewRelicShipper(name, opt)
	return w
}

func init() {
	RegisterEventWriter("newrelic", NewEventWriterNewRelic)
}

type nrItem struct {
	logger string
	data   []byte
}

type newRelicShipper struct {
	name     string
	opt      NewRelicOption
	client   *http.Client
	interval time.Duration
	timeout  time.Duration
	sleep    func(context.Context, time.Duration) bool // false when ctx is done

	maxEvents, maxBytes int

	disabled atomic.Bool // set after 401/403
	logged   atomic.Bool // set after the first "other 4xx" message

	mu     sync.Mutex
	buf    []nrItem
	size   int
	closed bool

	startOnce, closeOnce sync.Once
	ctx                  context.Context
	cancel               context.CancelFunc
	wake                 chan struct{}
	done                 chan struct{}
}

func newNewRelicShipper(name string, opt NewRelicOption) *newRelicShipper {
	rt := NewRelicTransport
	if rt == nil {
		rt = &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     90 * time.Second,
		}
	}
	return &newRelicShipper{
		name: name,
		opt:  opt,
		client: &http.Client{
			Transport: rt,
			// never follow redirects: Go would forward the Api-Key header to a same-domain target
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		interval:  5 * time.Second,
		timeout:   10 * time.Second,
		maxEvents: newRelicMaxEvents,
		maxBytes:  newRelicMaxBytes,
		sleep: func(ctx context.Context, d time.Duration) bool {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-t.C:
				return true
			case <-ctx.Done():
				return false
			}
		},
	}
}

func (s *newRelicShipper) start() {
	s.startOnce.Do(func() {
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.wake = make(chan struct{}, 1)
		s.done = make(chan struct{})
		go s.loop()
	})
}

func (s *newRelicShipper) count(items []nrItem, sent, dropped, errs bool) {
	per := map[string]uint64{}
	for _, it := range items {
		per[it.logger]++
	}
	for logger, n := range per {
		var a, b, c uint64
		if sent {
			a = n
		}
		if dropped {
			b = n
		}
		if errs {
			c = 1 // one error per failed attempt and logger
		}
		addShipping(s.name, logger, a, b, c)
	}
}

// Write only appends to the bounded buffer, so it never waits for the network.
func (s *newRelicShipper) Write(p []byte) (int, error) {
	logger, data, _ := strings.Cut(string(p), "\n")
	it := nrItem{logger, []byte(strings.TrimSuffix(data, "\n"))}
	if s.disabled.Load() {
		s.count([]nrItem{it}, false, true, false)
		return len(p), nil
	}
	s.start()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.count([]nrItem{it}, false, true, false)
		return len(p), nil
	}
	s.buf = append(s.buf, it)
	s.size += len(it.data)
	var dropped []nrItem
	for len(s.buf) > s.maxEvents || s.size > s.maxBytes {
		dropped = append(dropped, s.buf[0])
		s.size -= len(s.buf[0].data)
		s.buf = s.buf[1:]
	}
	full := s.size >= newRelicBatchBytes
	s.mu.Unlock()

	s.count(dropped, false, true, false)
	if full {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func (s *newRelicShipper) loop() {
	defer close(s.done)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
		s.flush(s.ctx, newRelicMaxAttempts)
	}
}

// take removes up to one batch from the buffer; when shipping is disabled it discards everything.
func (s *newRelicShipper) take() []nrItem {
	s.mu.Lock()
	n, size := 0, 0
	for n < len(s.buf) && (n == 0 || size+len(s.buf[n].data)+1 <= newRelicBatchBytes || s.disabled.Load()) {
		size += len(s.buf[n].data) + 1
		n++
	}
	items := s.buf[:n:n]
	s.buf = s.buf[n:]
	s.size -= size - n
	s.mu.Unlock()
	if s.disabled.Load() {
		s.count(items, false, true, false)
		return nil
	}
	return items
}

func (s *newRelicShipper) requeue(items []nrItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(slices.Clip(items), s.buf...)
	for _, it := range items {
		s.size += len(it.data)
	}
}

func (s *newRelicShipper) flush(ctx context.Context, attempts int) {
	for {
		items := s.take()
		if len(items) == 0 || !s.send(ctx, items, attempts) {
			return
		}
	}
}

func (s *newRelicShipper) encode(items []nrItem) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("["))
	for i, it := range items {
		if i > 0 {
			_, _ = zw.Write([]byte(","))
		}
		_, _ = zw.Write(it.data)
	}
	_, _ = zw.Write([]byte("]"))
	_ = zw.Close()
	return buf.Bytes()
}

func (s *newRelicShipper) post(ctx context.Context, body []byte) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.opt.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid request")
	}
	req.Header.Set("Api-Key", s.opt.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

func backoff(attempt int) time.Duration {
	d := min(time.Second<<attempt, 30*time.Second)
	return d + time.Duration((rand.Float64()*0.4-0.2)*float64(d))
}

// send reports false when the batch was put back because ctx ended.
func (s *newRelicShipper) send(ctx context.Context, items []nrItem, attempts int) bool {
	body := s.encode(items)
	for attempt := range attempts {
		code, err := s.post(ctx, body)
		switch {
		case err != nil && ctx.Err() != nil:
			s.requeue(items)
			return false
		case err == nil && code/100 == 2:
			s.count(items, true, false, false)
			return true
		case err == nil && (code == http.StatusUnauthorized || code == http.StatusForbidden):
			if s.disabled.CompareAndSwap(false, true) {
				FallbackErrorf("log writer %q: New Relic rejected the credentials (HTTP %d); shipping is disabled until restart", s.name, code)
			}
			s.count(items, false, true, true)
			return true
		case err == nil && code != http.StatusTooManyRequests && code < 500:
			if s.logged.CompareAndSwap(false, true) {
				FallbackErrorf("log writer %q: New Relic rejected a batch (HTTP %d); dropping it", s.name, code)
			}
			s.count(items, false, true, true)
			return true
		}
		s.count(items, false, false, true)
		if attempt < attempts-1 && !s.sleep(ctx, backoff(attempt)) {
			s.requeue(items)
			return false
		}
	}
	s.count(items, false, true, false)
	return true
}

// Close stops the flusher and makes one last attempt for what is buffered, within a short deadline.
func (s *newRelicShipper) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.cancel == nil {
			return
		}
		s.cancel()
		<-s.done
		ctx, cancel := context.WithTimeout(context.Background(), newRelicFinalTimeout)
		defer cancel()
		s.flush(ctx, 1)
		s.mu.Lock()
		rest := s.buf
		s.buf, s.size = nil, 0
		s.mu.Unlock()
		s.count(rest, false, true, false)
	})
	return nil
}
