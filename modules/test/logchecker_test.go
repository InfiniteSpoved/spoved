// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package test

import (
	"testing"
	"time"

	"forgejo.org/modules/log"

	"github.com/stretchr/testify/assert"
)

func TestLogCheckerInfo(t *testing.T) {
	lc, cleanup := NewLogChecker(log.DEFAULT, log.INFO)
	defer cleanup()

	lc.Filter("First", "Third").StopMark("End")
	log.Info("test")

	filtered, stopped := lc.Check(100 * time.Millisecond)
	assert.ElementsMatch(t, []bool{false, false}, filtered)
	assert.False(t, stopped)

	log.Info("First")
	log.Debug("Third")
	filtered, stopped = lc.Check(100 * time.Millisecond)
	assert.ElementsMatch(t, []bool{true, false}, filtered)
	assert.False(t, stopped)

	log.Info("Second")
	log.Debug("Third")
	filtered, stopped = lc.Check(100 * time.Millisecond)
	assert.ElementsMatch(t, []bool{true, false}, filtered)
	assert.False(t, stopped)

	log.Info("Third")
	filtered, stopped = lc.Check(100 * time.Millisecond)
	assert.ElementsMatch(t, []bool{true, true}, filtered)
	assert.False(t, stopped)

	log.Info("End")
	filtered, stopped = lc.Check(100 * time.Millisecond)
	assert.ElementsMatch(t, []bool{true, true}, filtered)
	assert.True(t, stopped)
}

func TestLogCheckerDebug(t *testing.T) {
	lc, cleanup := NewLogChecker(log.DEFAULT, log.DEBUG)
	defer cleanup()

	lc.StopMark("End")

	log.Debug("End")
	_, stopped := lc.Check(100 * time.Millisecond)
	assert.True(t, stopped)
}

// Without a stop mark an unrelated log event must not end the wait: it used to
// match the empty mark, so a background log line arriving before the one under
// test made Check return with the filters still unseen.
func TestLogCheckerNoStopMark(t *testing.T) {
	lc, cleanup := NewLogChecker(log.DEFAULT, log.INFO)
	defer cleanup()

	lc.Filter("Wanted")
	log.Info("unrelated")

	filtered, stopped := lc.Check(100 * time.Millisecond)
	assert.Equal(t, []bool{false}, filtered)
	assert.False(t, stopped)

	log.Info("Wanted")
	filtered, stopped = lc.Check(5 * time.Second) // returns as soon as every filter matched
	assert.Equal(t, []bool{true}, filtered)
	assert.False(t, stopped)
}
