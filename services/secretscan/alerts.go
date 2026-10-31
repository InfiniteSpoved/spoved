// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"unicode"

	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/util"
	vulnerability_service "forgejo.org/services/vulnerability"

	"golang.org/x/text/unicode/norm"
)

// ErrConflict is a change of an alert that its state doesn't allow any more (it changed meanwhile): the caller
// reloads and answers 409.
var ErrConflict = errors.New("the alert changed")

// ErrArchived is a change in an archived repository.
var ErrArchived = errors.New("the repository is archived")

// ErrCommentSecret is a comment the detector finds a secret in. Alert comments are stored, shown and audited, so
// they must not become a second place for the secret.
var ErrCommentSecret = util.NewInvalidArgumentErrorf("the comment looks like a secret")

// commentDetector is the detector of every rule, the generic ones too, for the comment of an alert.
var commentDetector = sync.OnceValues(func() (*secretdetection.Detector, error) {
	return secretdetection.NewDetector(secretdetection.BuiltinRules())
})

// checkComment refuses a comment the detector finds a secret in, or that holds the secret of an alert of the
// repository (fingerprints is the set of theirs): the rules don't see a secret that needs its context (an AWS secret
// key). The candidates are the words of the comment (also after "key=" or "key:", and without the characters around
// them that can't be in a secret, as in "**secret**" and "secret!"), every run of the characters a secret is made
// of (also without dots at the ends) and the parts of those runs between . / - # and : (any number of them from either end), each as it is, lower-cased
// and upper-cased, when it has 16 to 4096 bytes.
func checkComment(comment string, fingerprints map[string]struct{}) error {
	d, err := commentDetector()
	if err != nil {
		return err
	}
	if found, _ := d.Scan([]byte(comment)); len(found) > 0 {
		return ErrCommentSecret
	}
	if len(fingerprints) == 0 {
		return nil
	}
	tried := map[string]struct{}{}
	try := func(c string) bool {
		if len(c) < 16 || len(c) > 4096 {
			return false
		}
		for _, v := range []string{c, strings.ToLower(c), strings.ToUpper(c)} {
			if _, ok := tried[v]; ok {
				continue
			}
			tried[v] = struct{}{}
			if _, hit := fingerprints[secretdetection.Fingerprint([]byte(v))]; hit {
				return true
			}
		}
		return false
	}
	inSecret := func(r rune) bool {
		return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("+/=_-.", r))
	}
	tryWord := func(w string) bool { // the word, and what follows each = and :
		for rest := w; ; {
			if try(rest) {
				return true
			}
			i := strings.IndexAny(rest, "=:")
			if i < 0 {
				return false
			}
			rest = rest[i+1:]
		}
	}
	for w := range strings.FieldsFuncSeq(comment, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("\"'`,;()<>[]{}", r) }) {
		if tryWord(w) || tryWord(strings.TrimFunc(w, func(r rune) bool { return !inSecret(r) })) {
			return ErrCommentSecret
		}
	}
	for run := range strings.FieldsFuncSeq(comment, func(r rune) bool { return !inSecret(r) }) {
		if tryWord(run) || tryWord(strings.Trim(run, ".")) {
			return ErrCommentSecret
		}
		// what lies between the . / - # and : of the run (a secret can hold some of them: every start and end)
		starts, ends := []int{0}, []int{len(run)}
		for i, r := range run {
			if strings.ContainsRune("./-#:", r) {
				starts, ends = append(starts, i+1), append(ends, i)
			}
		}
		for _, from := range capped(starts) {
			for _, to := range capped(ends) {
				if from < to && tryWord(run[from:to]) {
					return ErrCommentSecret
				}
			}
		}
	}
	return nil
}

// capped keeps the first and the last 50 of the positions: a comment is short, but one made of delimiters would
// otherwise have as many candidates as the square of its length.
func capped(positions []int) []int {
	if len(positions) <= 100 {
		return positions
	}
	return slices.Concat(positions[:50], positions[len(positions)-50:])
}

// invisible removes what shows as nothing next to a letter: combining marks, format characters and fillers.
func invisible(r rune) rune {
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) || r == 0x115f || r == 0x1160 || r == 0x3164 || r == 0xffa0 { // and the Hangul fillers
		return -1
	}
	return r
}

// Close closes an open alert of the repository as revoked, false_positive, used_in_tests or wont_fix (the reason is the
// state), with an optional comment of at most 1,000 bytes. The audit entry is written in the same transaction. A
// bad state or comment is util.ErrInvalidArgument, an unknown alert util.ErrNotExist, an alert that isn't open
// ErrConflict, an archived repository ErrArchived.
func Close(ctx context.Context, repo *repo_model.Repository, doer *user_model.User, index int64, state, comment string) (*secretscan_model.Alert, error) {
	if !slices.Contains(secretscan_model.ClosedStates, state) {
		return nil, util.NewInvalidArgumentErrorf("resolution must be one of %v", secretscan_model.ClosedStates)
	}
	if len(comment) > 4*secretscan_model.MaxCommentLen {
		return nil, util.NewInvalidArgumentErrorf("the comment is longer than %d bytes", secretscan_model.MaxCommentLen)
	}
	// no control, bidi or other format characters (they hide a secret from the detector and reorder text)
	comment = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, vulnerability_service.CleanComment(comment))
	if len(comment) > secretscan_model.MaxCommentLen {
		return nil, util.NewInvalidArgumentErrorf("the comment is longer than %d bytes", secretscan_model.MaxCommentLen)
	}
	if comment != "" {
		// read outside the transaction, only for the check: the fingerprints of every alert of the repository (a comment
		// must hold none of their secrets); the transaction below changes the one alert with a conditional UPDATE
		fingerprints, err := secretscan_model.FindFingerprints(ctx, repo.ID)
		if err != nil {
			return nil, err
		}
		// the text as it reads: combining marks and format characters out, look-alikes folded (NFKC)
		for _, text := range []string{comment, strings.Map(invisible, norm.NFKC.String(comment))} {
			if err := checkComment(text, fingerprints); err != nil {
				return nil, err
			}
		}
	}
	return changeAlert(ctx, repo, "", func(ctx context.Context) (*secretscan_model.Alert, bool, error) {
		before, err := secretscan_model.CloseAlert(ctx, repo.ID, index, state, comment, doer.ID)
		return before, err == nil, err
	}, index, state, comment)
}

// Reopen reopens a closed alert; an open one is returned as it is.
func Reopen(ctx context.Context, repo *repo_model.Repository, index int64) (*secretscan_model.Alert, error) {
	return changeAlert(ctx, repo, "reopen", func(ctx context.Context) (*secretscan_model.Alert, bool, error) {
		return secretscan_model.ReopenAlert(ctx, repo.ID, index)
	}, index, secretscan_model.StateOpen, "")
}

func changeAlert(ctx context.Context, repo *repo_model.Repository, kind string, do func(context.Context) (*secretscan_model.Alert, bool, error), index int64, state, comment string) (*secretscan_model.Alert, error) {
	if repo.IsArchived {
		return nil, ErrArchived
	}
	var after *secretscan_model.Alert
	err := db.WithTx(ctx, func(ctx context.Context) error {
		before, changed, err := do(ctx)
		if err != nil {
			return err
		}
		if after, err = secretscan_model.GetAlert(ctx, repo.ID, index); err != nil {
			return err
		}
		if !changed {
			return nil
		}
		data := map[string]any{
			"index": before.Index, "rule": before.RuleID, "fingerprint": before.Fingerprint[:min(12, len(before.Fingerprint))],
			"previous_state": before.State, "state": state, "comment": comment,
		}
		if locs, err := secretscan_model.FindLocations(ctx, repo.ID, before.ID, 1); err != nil {
			return err
		} else if len(locs) > 0 {
			data["path"] = locs[0].Path
		}
		// the action keys are literals: TestActionCatalog finds them in the code
		ev := audit_model.Event{TargetType: "secret_scanning_alert", TargetID: before.ID, TargetName: before.RuleID, Data: data}
		if kind == "reopen" {
			ev.Action = "repo.secret_alert.reopened"
		} else {
			ev.Action = "repo.secret_alert.closed"
		}
		return repo_model.RecordRepoTarget(ctx, repo, ev)
	})
	switch {
	case errors.Is(err, secretscan_model.ErrAlertState):
		return nil, ErrConflict
	case err != nil:
		return nil, err
	}
	return after, nil
}
