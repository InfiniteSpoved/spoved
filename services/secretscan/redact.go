// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	activities_model "forgejo.org/models/activities"
	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	issue_indexer "forgejo.org/modules/indexer/issues"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"xorm.io/builder"
)

// RedactTarget names one text of an issue or pull request: its title or body (Issue is its number, Field "title" or
// "body"), or a comment (Comment is its ID, Field empty or "comment").
type RedactTarget struct {
	Issue, Comment int64
	Field          string
}

var (
	// ErrRedactTarget is a target that names no text.
	ErrRedactTarget = util.NewInvalidArgumentErrorf("name the title or body of an issue, or a comment")
	// ErrRedactGone is a text that no longer holds the secret of the alert: nothing is changed.
	ErrRedactGone = util.NewInvalidArgumentErrorf("the secret is no longer in this text")
	// ErrRedactManual is a text the detector can't point at the secret in (not plain text, or too costly to read whole).
	ErrRedactManual = util.NewInvalidArgumentErrorf("this text can't be redacted automatically: edit it")
	// ErrRedactChanged is a text that changed between its read and the write: nothing is changed, try again.
	ErrRedactChanged = errors.New("the text changed meanwhile")
)

// RedactedMark is what takes the place of the secret.
const RedactedMark = "[secret removed]"

// redact replaces every span of text where d finds the secret of the rule with that fingerprint. It returns the text,
// the spans it replaced and whether the detector read the text whole.
func redact(d *secretdetection.Detector, text, ruleID, fingerprint string) (string, int, bool) {
	if strings.IndexByte(text, 0) >= 0 || strings.HasPrefix(text, "\xff\xfe") || strings.HasPrefix(text, "\xfe\xff") {
		return text, 0, false // read as UTF-16, its offsets would not be those of text
	}
	findings, complete := scanContent(d, []byte(text))
	n := 0
	for _, f := range slices.Backward(findings) { // ordered by position and never overlapping: from the end, the offsets before stay right
		if f.RuleID == ruleID && f.Fingerprint == fingerprint {
			text = text[:f.Start] + RedactedMark + text[f.End:]
			n++
		}
	}
	return text, n, complete
}

// RedactText removes the secret of an alert from one text of an issue or pull request, for someone who may close the
// alert (the caller checks that) and reads the text (access). The secret is found again by the detector in the text
// as it is now (nothing of it is stored) and every span of it is replaced with RedactedMark. In one transaction the
// text is written back on condition that it did not change since it was read (else ErrRedactChanged), the earlier
// versions of its edit history are emptied (and, for a title, the secret in the title changes of the timeline and in
// the activity feed), the alert's places in it are marked removed and an audit entry is recorded. It runs no notifier:
// nobody is mailed and no webhook fires; the edit shows in the history as the doer's. A locked issue can be redacted.
// An unknown alert or text, a text the alert has no place in and one the doer can't read are util.ErrNotExist.
func RedactText(ctx context.Context, repo *repo_model.Repository, doer *user_model.User, index int64, target RedactTarget, access secretscan_model.TextAccess, canReadWiki bool) error {
	if repo.IsArchived {
		return ErrArchived
	}
	alert, err := secretscan_model.GetVisibleAlert(ctx, repo.ID, index, canReadWiki)
	if err != nil {
		return err
	}
	var issue *issues_model.Issue
	var comment *issues_model.Comment
	var src secretscan_model.TextSource
	switch {
	case target.Comment > 0 && (target.Field == "" || target.Field == "comment"):
		if comment, err = issues_model.GetCommentByID(ctx, target.Comment); err != nil {
			return err
		}
		if issue, err = issues_model.GetIssueByID(ctx, comment.IssueID); err != nil {
			return err
		}
		src = secretscan_model.TextSource{Kind: secretscan_model.KindComment, ID: comment.ID}
	case target.Comment == 0 && target.Issue > 0 && (target.Field == "title" || target.Field == "body"):
		if issue, err = issues_model.GetIssueByIndex(ctx, repo.ID, target.Issue); err != nil {
			return err
		}
		src = secretscan_model.TextSource{Kind: secretscan_model.KindIssue, ID: issue.ID, Field: target.Field}
	default:
		return ErrRedactTarget
	}
	// a text the doer can't read is missing, as one the alert is not in: the answer tells nothing about it
	if issue.RepoID != repo.ID || !(issue.IsPull && access.Pulls || !issue.IsPull && access.Issues) {
		return util.ErrNotExist
	}
	if has, err := secretscan_model.HasTextPlace(ctx, repo.ID, alert.ID, src); err != nil {
		return err
	} else if !has {
		return util.ErrNotExist
	}
	d, err := textDetector(ctx, repo, setting.SecretScanningSettings(ctx))
	if err != nil {
		return err
	}
	text := issue.Content
	switch {
	case comment != nil:
		text = comment.Content
	case src.Field == "title":
		text = issue.Title
	}
	redacted, n, complete := redact(d, text, alert.RuleID, alert.Fingerprint)
	switch {
	case n == 0 && !complete:
		return ErrRedactManual
	case n == 0:
		return ErrRedactGone
	}
	redactAll := func(s string) (string, bool) {
		out, n, _ := redact(d, s, alert.RuleID, alert.Fingerprint)
		return out, n > 0
	}
	err = db.WithTx(ctx, func(ctx context.Context) error {
		switch {
		case comment != nil:
			if err := clearContentHistory(ctx, issue.ID, comment.ID, comment.PosterID, comment.CreatedUnix); err != nil {
				return err
			}
			comment.Content = redacted
			if err := issues_model.UpdateComment(ctx, comment, comment.ContentVersion, doer); err != nil {
				return err
			}
			if err := issues_model.SaveIssueContentHistory(ctx, doer.ID, issue.ID, comment.ID, timeutil.TimeStampNow(), redacted, false); err != nil {
				return err
			}
			if err := redactFeed(ctx, builder.Eq{"repo_id": repo.ID, "comment_id": comment.ID}, redactAll); err != nil {
				return err
			}
		case src.Field == "title":
			old := issue.Title
			issue.Title, _ = util.SplitStringAtByteN(redacted, 255) // as ChangeIssueTitle: the mark may be longer than a short secret
			if affected, err := issues_model.UpdateIssueColsWithCond(ctx, issue, builder.Eq{"name": old}, "name"); err != nil {
				return err
			} else if affected == 0 {
				return ErrRedactChanged
			}
			if err := redactTitleChanges(ctx, issue.ID, redactAll); err != nil {
				return err
			}
			// the feed keeps the title of a new issue or pull request (and of a merge): ["<index>", "<title>", ...]
			prefix := `["` + strconv.FormatInt(issue.Index, 10) + `",`
			if err := redactFeed(ctx, builder.Eq{"repo_id": repo.ID}.And(builder.Like{"content", prefix + "%"}), redactAll); err != nil {
				return err
			}
		default:
			if err := clearContentHistory(ctx, issue.ID, 0, issue.PosterID, issue.CreatedUnix); err != nil {
				return err
			}
			if err := issues_model.ChangeIssueContent(ctx, issue, doer, redacted, issue.ContentVersion); err != nil {
				return err
			}
		}
		if err := secretscan_model.MarkTextPlacesRemoved(ctx, repo.ID, alert.ID, src); err != nil {
			return err
		}
		data := map[string]any{
			"index": alert.Index, "rule": alert.RuleID, "fingerprint": alert.Fingerprint[:min(12, len(alert.Fingerprint))],
			"issue": issue.Index, "field": util.Iif(comment != nil, "comment", src.Field), "replaced": n,
		}
		if comment != nil {
			data["comment_id"] = comment.ID
		}
		return repo_model.RecordRepoTarget(ctx, repo, audit_model.Event{
			Action: "repo.secret_alert.text_redacted", TargetType: "secret_scanning_alert", TargetID: alert.ID, TargetName: alert.RuleID, Data: data,
		})
	})
	if errors.Is(err, issues_model.ErrIssueAlreadyChanged) || errors.Is(err, issues_model.ErrCommentAlreadyChanged) {
		return ErrRedactChanged
	}
	if err != nil {
		return err
	}
	// after the commit: the search index drops the old text, and the text is scanned again (its other places may have
	// moved when a secret of several lines became one); a scan by nobody (0) mails nobody
	issue_indexer.UpdateIssueIndexer(ctx, issue.ID)
	if comment != nil {
		enqueueCommentText(ctx, comment, 0)
	} else {
		enqueueIssueText(ctx, issue.ID, 0)
	}
	return nil
}

// clearContentHistory empties every version in the edit history of a text (an issue's body: commentID 0). A text
// that was never edited gets the entry of its first version, empty, so the history shows that it was edited.
func clearContentHistory(ctx context.Context, issueID, commentID, posterID int64, created timeutil.TimeStamp) error {
	has, err := issues_model.HasIssueContentHistory(ctx, issueID, commentID)
	if err != nil {
		return err
	}
	if !has {
		if err := issues_model.SaveIssueContentHistory(ctx, posterID, issueID, commentID, created, "", true); err != nil {
			return err
		}
	}
	_, err = db.GetEngine(ctx).Where("issue_id = ? AND comment_id = ?", issueID, commentID).Cols("is_deleted", "content_text").
		Update(&issues_model.ContentHistory{IsDeleted: true})
	return err
}

// redactTitleChanges removes the secret from the title changes of the issue's timeline.
func redactTitleChanges(ctx context.Context, issueID int64, redactAll func(string) (string, bool)) error {
	var changes []*issues_model.Comment
	if err := db.GetEngine(ctx).Where("issue_id = ? AND type = ?", issueID, issues_model.CommentTypeChangeTitle).Cols("id", "old_title", "new_title").Find(&changes); err != nil {
		return err
	}
	for _, c := range changes {
		oldTitle, a := redactAll(c.OldTitle)
		newTitle, b := redactAll(c.NewTitle)
		if a || b {
			if _, err := db.GetEngine(ctx).ID(c.ID).Cols("old_title", "new_title").NoAutoTime().Update(&issues_model.Comment{OldTitle: oldTitle, NewTitle: newTitle}); err != nil {
				return err
			}
		}
	}
	return nil
}

// redactFeed removes the secret from the activity feed's copies of a text.
func redactFeed(ctx context.Context, cond builder.Cond, redactAll func(string) (string, bool)) error {
	var actions []*activities_model.Action
	if err := db.GetEngine(ctx).Where(cond).Cols("id", "content").Find(&actions); err != nil {
		return err
	}
	for _, a := range actions {
		if content, ok := redactAll(a.Content); ok {
			if _, err := db.GetEngine(ctx).ID(a.ID).Cols("content").Update(&activities_model.Action{Content: content}); err != nil {
				return err
			}
		}
	}
	return nil
}
