// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package security

import (
	"errors"
	"html"
	"net/http"
	"strings"

	secretscan_model "forgejo.org/models/secretscan"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/services/context"
	secretscan_service "forgejo.org/services/secretscan"
)

// PatternRow is a pattern of the page. Text is the pattern itself: empty unless the viewer may read it (owners of the
// pattern's level), the others see the name and the keywords.
type PatternRow struct {
	*secretscan_model.CustomPattern
	Keywords  []string
	Text      string
	Site      bool // a site pattern on an organization's page: shown, never changed there
	CreatedBy string
	Alerts    int64 // open alerts the viewer sees
}

// PatternForm is what the add form sends, kept for a refused save. The sample is never kept.
type PatternForm struct {
	Name     string
	Pattern  string
	Keywords string
}

// PatternErr is the refusal of a save, shown above the form; Field is the form field to mark.
type PatternErr struct {
	Title string
	Text  string
	Field string
}

// PatternsPage is the data of the patterns box, as ctx.Data["Patterns"].
type PatternsPage struct {
	Link, ListLink string // the box posts to Link; ListLink is the alert list, which takes ?type=
	Rows           []PatternRow
	CanWrite       bool
	ShowText       bool
	Locked         string // why a viewer who could write can't: "archived" or "maintenance"
	On, Cap        int    // the patterns that are on, and how many may be (the site's count in an organization)
	Form           PatternForm
	Err            *PatternErr
}

// LoadPatterns sets ctx.Data["Patterns"]. org is nil for the site's page. scope decides which alerts the counts
// cover. A refused add keeps its form and error from ctx.Data["PatternForm"] and ["PatternErr"].
func LoadPatterns(ctx *context.Context, org *user_model.User, scope secretscan_service.Scope, link, listLink string, canWrite, showText bool) error {
	owners := []int64{0}
	if org != nil {
		owners = append(owners, org.ID)
	}
	patterns, err := secretscan_model.FindLiveCustomPatterns(ctx, owners...)
	if err != nil {
		return err
	}
	var ruleIDs []string
	var userIDs []int64
	for _, p := range patterns {
		ruleIDs = append(ruleIDs, p.RuleID())
		userIDs = append(userIDs, p.CreatedByID)
	}
	users, err := user_model.GetUsersByIDsWithBots(ctx, userIDs)
	if err != nil {
		return err
	}
	counts := map[string]int64{}
	if cond, empty, err := scope.RepoCond(ctx); err != nil {
		return err
	} else if !empty {
		if counts, err = secretscan_model.CountOpenAlertsByRule(ctx, cond, ruleIDs); err != nil {
			return err
		}
	}
	on, err := secretscan_model.CountActiveCustomPatterns(ctx, owners...)
	if err != nil {
		return err
	}
	page := &PatternsPage{Link: link, ListLink: listLink, CanWrite: canWrite, ShowText: showText, On: int(on), Cap: secretdetection.MaxCustomRules}
	for _, p := range patterns {
		row := PatternRow{CustomPattern: p, Keywords: p.KeywordList(), Site: p.OwnerID == 0 && org != nil, Alerts: counts[p.RuleID()]}
		if showText && !row.Site {
			row.Text = p.Pattern
		}
		if _, u := user_model.GetUserFromMap(p.CreatedByID, users); u != nil && p.CreatedByID > 0 {
			row.CreatedBy = u.GetDisplayName()
		}
		page.Rows = append(page.Rows, row)
	}
	switch {
	case org != nil && org.IsArchived:
		page.Locked = "archived"
	case setting.GetMaintenance(ctx).ReadOnly():
		page.Locked = "maintenance"
	}
	if f, ok := ctx.Data["PatternForm"].(PatternForm); ok {
		page.Form = f
	}
	if e, ok := ctx.Data["PatternErr"].(*PatternErr); ok {
		page.Err = e
	}
	if page.Locked != "" {
		page.CanWrite = false
	}
	ctx.Data["Patterns"] = page
	return nil
}

func splitKeywords(s string) []string {
	var out []string
	for k := range strings.SplitSeq(s, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

func patternInput(ctx *context.Context) secretscan_service.PatternInput {
	return secretscan_service.PatternInput{
		Name: ctx.FormString("name"), Pattern: ctx.FormString("pattern"),
		Keywords: splitKeywords(ctx.FormString("keywords")), Sample: ctx.FormString("sample"),
	}
}

// patternRefusal says why a save or a test was refused: a locale key under secret_pattern.err_ (title and text), the
// form field to mark, and the text of the engine's reason where the key takes one.
func patternRefusal(err error) (key, field, detail string) {
	switch {
	case errors.Is(err, secretdetection.ErrCustomNameInvalid):
		return "name", "name", ""
	case errors.Is(err, secretscan_service.ErrPatternNameTaken):
		return "name_taken", "name", ""
	case errors.Is(err, secretdetection.ErrPatternTooLong):
		return "too_long", "pattern", ""
	case errors.Is(err, secretdetection.ErrPatternInvalid):
		return "invalid", "pattern", strings.TrimPrefix(err.Error(), secretdetection.ErrPatternInvalid.Error()+": ")
	case errors.Is(err, secretdetection.ErrPatternTooComplex):
		return "complex", "pattern", ""
	case errors.Is(err, secretdetection.ErrPatternMatchesEmpty):
		return "empty", "pattern", ""
	case errors.Is(err, secretdetection.ErrPatternMatchesTooMuch):
		return "common", "pattern", ""
	case errors.Is(err, secretdetection.ErrKeywordMissing):
		return "keywords", "keywords", ""
	case errors.Is(err, secretdetection.ErrKeywordTooCommon):
		return "keyword_common", "keywords", ""
	case errors.Is(err, secretdetection.ErrPatternHoldsSecret):
		return "holds_secret", "pattern", ""
	case errors.Is(err, secretdetection.ErrPatternNoMatchInTest):
		return "no_match", "sample", ""
	case errors.Is(err, secretdetection.ErrSecretTooShort):
		return "short", "sample", ""
	case errors.Is(err, secretdetection.ErrTestStringTooLong):
		return "sample_long", "sample", ""
	case errors.Is(err, secretscan_service.ErrPatternLimit):
		return "limit", "", ""
	}
	return "", "", ""
}

// refusal builds the PatternErr of an error. The texts take the masked match (never the sample) and the limit.
func refusal(ctx *context.Context, err error, res *secretscan_service.TestResult) *PatternErr {
	key, field, detail := patternRefusal(err)
	if key == "" {
		return nil
	}
	var args []any
	switch {
	case key == "invalid":
		args = []any{detail}
	case key == "limit":
		args = []any{secretdetection.MaxCustomRules}
	case (key == "keywords" || key == "short") && res != nil:
		key += "_found"
		args = []any{res.Masked}
		if key == "short_found" {
			args = append(args, res.SecretLen)
		}
	}
	return &PatternErr{
		Field: field, Title: ctx.Locale.TrString("secret_pattern.err_title." + key),
		Text: ctx.Locale.TrString("secret_pattern.err_text."+key, args...),
	}
}

// PatternTest checks a pattern against a sample and answers JSON; nothing is stored, and the answer holds at most the
// first characters of what was found. It reads and runs a user's expression, so the route is rate limited.
func PatternTest(ctx *context.Context) {
	res, err := secretscan_service.TestPattern(patternInput(ctx))
	if err == nil {
		ctx.JSON(http.StatusOK, map[string]any{
			"ok":      true,
			"title":   ctx.Locale.TrString("secret_pattern.test_ok.title"),
			"message": ctx.Locale.TrString("secret_pattern.test_ok.text", res.SecretLen, res.Masked),
		})
		return
	}
	e := refusal(ctx, err, res)
	if e == nil {
		ctx.JSON(http.StatusOK, map[string]any{"ok": false, "title": ctx.Locale.TrString("secret_pattern.err_title.failed"), "message": ""})
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"ok": false, "title": e.Title, "message": e.Text, "field": e.Field})
}

// PatternAdd saves the posted pattern for the organization (nil: the site) and says whether it did. A refusal sets
// ctx.Data["PatternErr"] and ["PatternForm"] for the page to show again (the sample is not kept).
func PatternAdd(ctx *context.Context, org *user_model.User) bool {
	in := patternInput(ctx)
	_, err := secretscan_service.CreatePattern(ctx, ctx.Doer, patternOwner(org), in)
	if err == nil {
		ctx.Flash.Success(ctx.Tr("secret_pattern.added", strings.TrimSpace(in.Name)))
		return true
	}
	res, _ := secretscan_service.TestPattern(in)
	e := refusal(ctx, err, res)
	if e == nil {
		ctx.ServerError("CreatePattern", err)
		return false
	}
	ctx.Data["PatternErr"] = e
	ctx.Data["PatternForm"] = PatternForm{Name: in.Name, Pattern: in.Pattern, Keywords: ctx.FormString("keywords")}
	return false
}

// PatternAction runs one of rename, enable, disable and delete on the pattern of the route and redirects to page.
func PatternAction(ctx *context.Context, org *user_model.User, action, page string) {
	id := ctx.ParamsInt64("id")
	var err error
	switch action {
	case "rename":
		err = secretscan_service.RenamePattern(ctx, patternOwner(org), id, ctx.FormString("name"))
	case "enable":
		err = secretscan_service.SetPatternEnabled(ctx, patternOwner(org), id, true)
	case "disable":
		err = secretscan_service.SetPatternEnabled(ctx, patternOwner(org), id, false)
	case "delete":
		err = deletePattern(ctx, org, id)
	}
	switch {
	case errors.Is(err, secretscan_service.ErrPatternNotFound):
		ctx.NotFound("pattern", nil)
		return
	case err == nil:
		ctx.Flash.Success(ctx.Tr("secret_pattern.done." + action))
	case errors.Is(err, errConfirmName):
		ctx.Flash.Error(ctx.Tr("secret_pattern.delete_confirm_failed"))
	default:
		e := refusal(ctx, err, nil)
		if e == nil {
			ctx.ServerError("pattern "+action, err)
			return
		}
		ctx.Flash.Error(html.EscapeString(e.Title + ": " + e.Text)) // Flash.Error renders sanitized HTML
	}
	ctx.Redirect(page)
}

var errConfirmName = util.NewInvalidArgumentErrorf("the name does not match")

// patternOwner is the site for a nil organization.
func patternOwner(org *user_model.User) secretscan_service.PatternOwner {
	if org == nil {
		return secretscan_service.SitePatterns
	}
	return secretscan_service.OrgPatterns(org)
}

// deletePattern deletes after the typed name matched the pattern's.
func deletePattern(ctx *context.Context, org *user_model.User, id int64) error {
	var owner int64
	if org != nil {
		owner = org.ID
	}
	p, err := secretscan_model.GetCustomPattern(ctx, owner, id)
	if err != nil {
		return err
	}
	if p == nil || p.DeletedUnix != 0 {
		return secretscan_service.ErrPatternNotFound
	}
	if !strings.EqualFold(strings.TrimSpace(ctx.FormString("confirm_name")), p.Name) {
		return errConfirmName
	}
	return secretscan_service.DeletePattern(ctx, patternOwner(org), id)
}

// customSecretTypes are the choices of the type filter for custom patterns: the site's and, in an organization's
// list, the organization's, on or off. Names only. A deleted pattern's alerts are still listed, just not offered.
func customSecretTypes(ctx *context.Context, scope secretscan_service.Scope) ([]SecretType, error) {
	owners := []int64{0}
	if scope.Org != nil {
		owners = append(owners, scope.Org.ID)
	}
	patterns, err := secretscan_model.FindLiveCustomPatterns(ctx, owners...)
	if err != nil {
		return nil, err
	}
	out := make([]SecretType, len(patterns))
	for i, p := range patterns {
		out[i] = SecretType{p.RuleID(), p.Name}
	}
	return out, nil
}
