// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package context

import (
	"context"
	"errors"
	"net/http"
	"strings"

	quota_model "forgejo.org/models/quota"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	repo_module "forgejo.org/modules/repository"
)

type QuotaTargetType int

const (
	QuotaTargetUser QuotaTargetType = iota
	QuotaTargetRepo
	QuotaTargetOrg
)

// QuotaExceeded
// swagger:response quotaExceeded
type APIQuotaExceeded struct {
	Message  string `json:"message"`
	UserID   int64  `json:"user_id"`
	UserName string `json:"username,omitempty"`
}

// QuotaGroupAssignmentAPI returns a middleware to handle context-quota-group assignment for api routes
func QuotaGroupAssignmentAPI() func(ctx *APIContext) {
	return func(ctx *APIContext) {
		groupName := ctx.Params("quotagroup")
		group, err := quota_model.GetGroupByName(ctx, groupName)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "quota_model.GetGroupByName", err)
			return
		}
		if group == nil {
			ctx.NotFound()
			return
		}
		ctx.quotaGroup = group
	}
}

// QuotaRuleAssignmentAPI returns a middleware to handle context-quota-rule assignment for api routes
func QuotaRuleAssignmentAPI() func(ctx *APIContext) {
	return func(ctx *APIContext) {
		ruleName := ctx.Params("quotarule")
		rule, err := quota_model.GetRuleByName(ctx, ruleName)
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "quota_model.GetRuleByName", err)
			return
		}
		if rule == nil {
			ctx.NotFound()
			return
		}
		ctx.quotaRule = rule
	}
}

// ctx.CheckQuota checks whether the user in question is within quota limits (web context)
func (ctx *Context) CheckQuota(subject quota_model.LimitSubject, userID int64, username string) bool {
	return ctx.checkQuota(quota_model.EvaluateForUser, subject, userID, username)
}

// EnforceQuotaGroupsWeb is EnforceQuotaWeb without the size limit of personal repositories, for the web deletes
// that stay allowed at that limit.
func EnforceQuotaGroupsWeb(subject quota_model.LimitSubject, target QuotaTargetType) func(ctx *Context) {
	return func(ctx *Context) {
		ctx.checkQuota(quota_model.EvaluateQuotaGroupsForUser, subject, target.UserID(ctx), target.UserName(ctx))
	}
}

func (ctx *Context) checkQuota(evaluate quotaEvaluator, subject quota_model.LimitSubject, userID int64, username string) bool {
	ok, err := checkQuota(ctx.originCtx, evaluate, subject, userID, username, func(userID int64, username string) {
		personal := personalRepoLimitReached(ctx, userID)
		showHTML := false
		for _, part := range ctx.Req.Header["Accept"] {
			if strings.Contains(part, "text/html") {
				showHTML = true
				break
			}
		}
		if !showHTML {
			msg := "Quota exceeded."
			if personal.Reached() {
				msg = personal.Message()
			}
			ctx.plainTextInternal(3, http.StatusRequestEntityTooLarge, []byte(msg+"\n"))
			return
		}

		if personal.Reached() {
			ctx.Data["QuotaMessage"] = ctx.Tr("repo.personal_repos.limit_reached", base.FileSize(personal.Used), base.FileSize(personal.Limit))
		}
		ctx.Data["IsRepo"] = ctx.Repo.Repository != nil
		ctx.Data["Title"] = "Quota Exceeded"
		ctx.HTML(http.StatusRequestEntityTooLarge, base.TplName("status/413"))
	}, func(err error) {
		ctx.Error(http.StatusInternalServerError, "quota_model.EvaluateForUser")
	})
	if err != nil {
		return false
	}
	return ok
}

// ctx.CheckQuota checks whether the user in question is within quota limits (API context)
func (ctx *APIContext) CheckQuota(subject quota_model.LimitSubject, userID int64, username string) bool {
	ok, err := checkQuota(ctx.originCtx, quota_model.EvaluateForUser, subject, userID, username, func(userID int64, username string) {
		msg := "quota exceeded"
		if personal := personalRepoLimitReached(ctx, userID); personal.Reached() {
			msg = personal.Message()
		}
		ctx.JSON(http.StatusRequestEntityTooLarge, APIQuotaExceeded{
			Message:  msg,
			UserID:   userID,
			UserName: username,
		})
	}, func(err error) {
		ctx.InternalServerError(err)
	})
	if err != nil {
		return false
	}
	return ok
}

// EnforceQuotaWeb returns a middleware that enforces quota limits on the given web route.
func EnforceQuotaWeb(subject quota_model.LimitSubject, target QuotaTargetType) func(ctx *Context) {
	return func(ctx *Context) {
		ctx.CheckQuota(subject, target.UserID(ctx), target.UserName(ctx))
	}
}

// EnforceQuotaWeb returns a middleware that enforces quota limits on the given API route.
func EnforceQuotaAPI(subject quota_model.LimitSubject, target QuotaTargetType) func(ctx *APIContext) {
	return func(ctx *APIContext) {
		ctx.CheckQuota(subject, target.UserID(ctx), target.UserName(ctx))
	}
}

// checkQuota wraps quota checking into a single function
type quotaEvaluator func(ctx context.Context, userID int64, subject quota_model.LimitSubject) (bool, error)

func checkQuota(ctx context.Context, evaluate quotaEvaluator, subject quota_model.LimitSubject, userID int64, username string, quotaExceededHandler func(userID int64, username string), errorHandler func(err error)) (bool, error) {
	ok, err := evaluate(ctx, userID, subject)
	if err != nil {
		errorHandler(err)
		return false, err
	}
	if !ok {
		quotaExceededHandler(userID, username)
		return false, nil
	}
	return true, nil
}

// personalRepoLimitReached returns the personal repository size limit of userID, for the refusal message; an error
// reads as not reached, so the generic quota message is shown.
func personalRepoLimitReached(ctx context.Context, userID int64) quota_model.PersonalRepoLimit {
	l, err := quota_model.GetPersonalRepoLimit(ctx, userID)
	if err != nil {
		log.Error("GetPersonalRepoLimit: %v", err)
	}
	return l
}

type QuotaContext interface {
	GetQuotaTargetUserID(target QuotaTargetType) int64
	GetQuotaTargetUserName(target QuotaTargetType) string
}

func (ctx *Context) GetQuotaTargetUserID(target QuotaTargetType) int64 {
	switch target {
	case QuotaTargetUser:
		return ctx.Doer.ID
	case QuotaTargetRepo:
		return ctx.Repo.Repository.OwnerID
	case QuotaTargetOrg:
		return ctx.Org.Organization.ID
	default:
		return 0
	}
}

func (ctx *Context) GetQuotaTargetUserName(target QuotaTargetType) string {
	switch target {
	case QuotaTargetUser:
		return ctx.Doer.Name
	case QuotaTargetRepo:
		return ctx.Repo.Repository.Owner.Name
	case QuotaTargetOrg:
		return ctx.Org.Organization.Name
	default:
		return ""
	}
}

func (ctx *APIContext) GetQuotaTargetUserID(target QuotaTargetType) int64 {
	switch target {
	case QuotaTargetUser:
		return ctx.Doer().ID
	case QuotaTargetRepo:
		return ctx.Repo().Repository.OwnerID
	case QuotaTargetOrg:
		return ctx.Org().Organization.ID
	default:
		return 0
	}
}

func (ctx *APIContext) GetQuotaTargetUserName(target QuotaTargetType) string {
	switch target {
	case QuotaTargetUser:
		return ctx.Doer().Name
	case QuotaTargetRepo:
		return ctx.Repo().Repository.Owner.Name
	case QuotaTargetOrg:
		return ctx.Org().Organization.Name
	default:
		return ""
	}
}

func (target QuotaTargetType) UserID(ctx QuotaContext) int64 {
	return ctx.GetQuotaTargetUserID(target)
}

func (target QuotaTargetType) UserName(ctx QuotaContext) string {
	return ctx.GetQuotaTargetUserName(target)
}

// PersonalRepoRefusal returns the translated message of a refusal by the personal repository policy or size limit
// in err, and false for any other error.
func (ctx *Context) PersonalRepoRefusal(err error) (string, bool) {
	if e, ok := errors.AsType[repo_module.ErrPersonalRepoPolicy](err); ok {
		return ctx.Locale.TrString(e.TrKey()), true
	}
	if e, ok := errors.AsType[quota_model.ErrPersonalRepoLimit](err); ok {
		return ctx.Locale.TrString("repo.personal_repos.limit_reached", base.FileSize(e.Used), base.FileSize(e.Limit)), true
	}
	return "", false
}
