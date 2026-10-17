// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package audit

import (
	"fmt"
	"testing"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sealed(t *testing.T, n int) []*audit_model.Entry {
	t.Helper()
	require.NoError(t, unittest.PrepareTestDatabase())
	insertUnsealed(t, n)
	require.NoError(t, SealAll(t.Context()))
	var rows []*audit_model.Entry // rows[i] has seq i+1
	require.NoError(t, db.GetEngine(t.Context()).Where("seq IS NOT NULL").OrderBy("seq").Find(&rows))
	return rows
}

func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := db.GetEngine(t.Context()).Exec(append([]any{sql}, args...)...)
	require.NoError(t, err)
}

func verifyFails(t *testing.T, opts VerifyOptions, seq int64, reason string) {
	t.Helper()
	res, err := Verify(t.Context(), opts)
	require.NoError(t, err)
	require.NotNil(t, res.Failure)
	assert.Equal(t, VerifyFailure{Seq: seq, Reason: reason}, *res.Failure)
}

// AC4
func TestVerify(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		rows := sealed(t, 5)
		insertUnsealed(t, 2)
		h := hookSQL(t)
		res, err := Verify(t.Context(), VerifyOptions{})
		require.NoError(t, err)
		assert.Nil(t, res.Failure)
		assert.EqualValues(t, 6, res.Rows)
		assert.EqualValues(t, 1, res.FirstSeq)
		assert.EqualValues(t, 6, res.LastSeq)
		assert.Equal(t, *rows[len(rows)-1].Hash, res.LastHash)
		assert.EqualValues(t, 2, res.Unsealed)
		assert.NotZero(t, res.OldestUnsealedAge)
		assert.Empty(t, h.writes())
	})
	t.Run("edited", func(t *testing.T) {
		rows := sealed(t, 5)
		exec(t, "UPDATE audit_event SET data = ? WHERE id = ?", `{"i":99}`, rows[2].ID)
		verifyFails(t, VerifyOptions{}, *rows[2].Seq, "hash")
	})
	t.Run("deleted middle", func(t *testing.T) {
		rows := sealed(t, 5)
		exec(t, "DELETE FROM audit_event WHERE id = ?", rows[2].ID)
		verifyFails(t, VerifyOptions{}, *rows[2].Seq, "gap")
	})
	t.Run("deleted tail", func(t *testing.T) {
		rows := sealed(t, 5)
		exec(t, "DELETE FROM audit_event WHERE id = ?", rows[len(rows)-1].ID)
		verifyFails(t, VerifyOptions{}, *rows[len(rows)-1].Seq, "head")
	})
	t.Run("swapped", func(t *testing.T) {
		rows := sealed(t, 5)
		exec(t, "UPDATE audit_event SET seq = 1000 WHERE id = ?", rows[1].ID)
		exec(t, "UPDATE audit_event SET seq = ? WHERE id = ?", *rows[1].Seq, rows[2].ID)
		exec(t, "UPDATE audit_event SET seq = ? WHERE id = ?", *rows[2].Seq, rows[1].ID)
		res, err := Verify(t.Context(), VerifyOptions{})
		require.NoError(t, err)
		require.NotNil(t, res.Failure)
		assert.Contains(t, []string{"link", "hash"}, res.Failure.Reason)
	})
	t.Run("anchor", func(t *testing.T) {
		rows := sealed(t, 5)
		good := fmt.Sprintf("%d:%s", *rows[2].Seq, *rows[2].Hash)
		res, err := Verify(t.Context(), VerifyOptions{Anchor: good})
		require.NoError(t, err)
		assert.Nil(t, res.Failure)

		verifyFails(t, VerifyOptions{Anchor: fmt.Sprintf("%d:%s", *rows[2].Seq, audit_model.ZeroHash)}, *rows[2].Seq, "anchor")
		verifyFails(t, VerifyOptions{Anchor: fmt.Sprintf("99:%s", audit_model.ZeroHash)}, 99, "anchor") // above the last row
		verifyFails(t, VerifyOptions{Anchor: good, From: 4}, *rows[2].Seq, "anchor")                    // below --from
		_, err = Verify(t.Context(), VerifyOptions{Anchor: "nonsense"})
		require.Error(t, err)
	})
	t.Run("from", func(t *testing.T) {
		rows := sealed(t, 5)
		res, err := Verify(t.Context(), VerifyOptions{From: 3})
		require.NoError(t, err)
		assert.Nil(t, res.Failure)
		assert.EqualValues(t, 3, res.FirstSeq)
		assert.EqualValues(t, 4, res.Rows)

		exec(t, "UPDATE audit_event SET prev_hash = ? WHERE id = ?", audit_model.ZeroHash, rows[2].ID) // row 3 of the chain
		verifyFails(t, VerifyOptions{From: 3}, 3, "link")
	})
}

// A head that was set back, even to 0, leaves sealed rows beyond it.
func TestVerifyHeadSetBack(t *testing.T) {
	sealed(t, 5)
	exec(t, "UPDATE audit_chain_head SET seq = 0, hash = ?", audit_model.ZeroHash)
	verifyFails(t, VerifyOptions{}, 0, "head")
}
