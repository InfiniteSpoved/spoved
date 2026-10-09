// Copyright 2026 The Forgejo Authors.
// SPDX-License-Identifier: GPL-3.0-or-later

package log

import (
	"sort"
	"sync"
)

// ShippingStat counts the events a shipping writer sent, dropped and failed to send for one logger.
type ShippingStat struct {
	Writer, Logger        string
	Sent, Dropped, Errors uint64
}

var (
	shippingMu    sync.Mutex
	shippingStats = map[[2]string]*ShippingStat{}
)

func addShipping(writer, logger string, sent, dropped, errs uint64) {
	shippingMu.Lock()
	defer shippingMu.Unlock()
	st := shippingStats[[2]string{writer, logger}]
	if st == nil {
		st = &ShippingStat{Writer: writer, Logger: logger}
		shippingStats[[2]string{writer, logger}] = st
	}
	st.Sent += sent
	st.Dropped += dropped
	st.Errors += errs
}

// ShippingStatsSnapshot returns a sorted copy of the counters, for the metrics collector.
func ShippingStatsSnapshot() []ShippingStat {
	shippingMu.Lock()
	defer shippingMu.Unlock()
	out := make([]ShippingStat, 0, len(shippingStats))
	for _, st := range shippingStats {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Writer != out[j].Writer {
			return out[i].Writer < out[j].Writer
		}
		return out[i].Logger < out[j].Logger
	})
	return out
}
