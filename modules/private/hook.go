// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"forgejo.org/modules/git"
	"forgejo.org/modules/git/pushoptions"
	"forgejo.org/modules/log"
	"forgejo.org/modules/repository"
)

// Git environment variables
const (
	GitAlternativeObjectDirectories = "GIT_ALTERNATE_OBJECT_DIRECTORIES"
	GitObjectDirectory              = "GIT_OBJECT_DIRECTORY"
	GitQuarantinePath               = "GIT_QUARANTINE_PATH"
)

// HookOptions represents the options for the Hook calls
type HookOptions struct {
	OldCommitIDs                    []string
	NewCommitIDs                    []string
	RefFullNames                    []git.RefName
	UserID                          int64
	UserName                        string
	GitObjectDirectory              string
	GitAlternativeObjectDirectories string
	GitQuarantinePath               string
	GitPushOptions                  map[string]string
	PullRequestID                   int64
	PushTrigger                     repository.PushTrigger
	DeployKeyKeyID                  int64 // public key ID if the pusher is a deploy key; UserID is then the repo's owner.
	IsWiki                          bool
	ActionTaskID                    int64 // the Actions task whose token pushes (UserID is the Actions user)
	// who pushes, for the audit log: set by the server (serv, git HTTP, web pushes) and checked again by the hook
	AuditCredential, AuditIP, AuditRequestID string
}

func (o *HookOptions) GetGitPushOptions() pushoptions.Interface {
	return pushoptions.NewFromMap(&o.GitPushOptions)
}

// SSHLogOption ssh log options
type SSHLogOption struct {
	Level   log.Level
	Message string
}

// HookPostReceiveResult represents an individual result from PostReceive
type HookPostReceiveResult struct {
	Results      []HookPostReceiveBranchResult
	RepoWasEmpty bool
	Err          string
}

// HookPostReceiveBranchResult represents an individual branch result from PostReceive
type HookPostReceiveBranchResult struct {
	Message   bool
	Create    bool
	Branch    string
	CreateURL string
	PullURLS  []string
}

// HookProcReceiveResult represents an individual result from ProcReceive
type HookProcReceiveResult struct {
	Results []HookProcReceiveRefResult
	Err     string
}

// HookProcReceiveRefResult represents an individual result from ProcReceive
type HookProcReceiveRefResult struct {
	OldOID       string
	NewOID       string
	Ref          string
	OriginalRef  git.RefName
	IsForcePush  bool
	IsNotMatched bool
	Err          string
}

// HookPreReceive check whether the provided commits are allowed
func HookPreReceive(ctx context.Context, repoID int64, opts HookOptions) ResponseExtra {
	reqURL := fmt.Sprintf("/api/internal/hook/pre-receive/%d", repoID)
	req := newInternalRequest(ctx, reqURL, "POST", opts)
	req.SetReadWriteTimeout(time.Duration(60+len(opts.OldCommitIDs)) * time.Second)
	_, extra := requestJSONResp(req, &ResponseText{})
	return extra
}

// HookPostReceive updates services and users
func HookPostReceive(ctx context.Context, repoID int64, opts HookOptions) (*HookPostReceiveResult, ResponseExtra) {
	reqURL := fmt.Sprintf("/api/internal/hook/post-receive/%d", repoID)
	req := newInternalRequest(ctx, reqURL, "POST", opts)
	req.SetReadWriteTimeout(time.Duration(60+len(opts.OldCommitIDs)) * time.Second)
	return requestJSONResp(req, &HookPostReceiveResult{})
}

// HookPushAudit records the push in the audit log once, after post-receive, with all of its refs
func HookPushAudit(ctx context.Context, repoID int64, opts HookOptions) ResponseExtra {
	reqURL := fmt.Sprintf("/api/internal/hook/push-audit/%d", repoID)
	req := newInternalRequest(ctx, reqURL, "POST", opts)
	req.SetReadWriteTimeout(time.Duration(60+len(opts.OldCommitIDs)/30) * time.Second)
	_, extra := requestJSONResp(req, &ResponseText{})
	return extra
}

// HookProcReceive proc-receive hook
func HookProcReceive(ctx context.Context, repoID int64, opts HookOptions) (*HookProcReceiveResult, ResponseExtra) {
	reqURL := fmt.Sprintf("/api/internal/hook/proc-receive/%d", repoID)

	req := newInternalRequest(ctx, reqURL, "POST", opts)
	req.SetReadWriteTimeout(time.Duration(60+len(opts.OldCommitIDs)) * time.Second)
	return requestJSONResp(req, &HookProcReceiveResult{})
}

// SetDefaultBranch will set the default branch to the provided branch for the provided repository
func SetDefaultBranch(ctx context.Context, repoID int64, branch string) ResponseExtra {
	reqURL := fmt.Sprintf("/api/internal/hook/set-default-branch/%d/%s", repoID, url.PathEscape(branch))
	req := newInternalRequest(ctx, reqURL, "POST")
	_, extra := requestJSONResp(req, &ResponseText{})
	return extra
}

// SSHLog sends ssh error log response
func SSHLog(ctx context.Context, level log.Level, msg string) error {
	req := newInternalRequest(ctx, "/api/internal/ssh/log", "POST", &SSHLogOption{Level: level, Message: msg})
	_, extra := requestJSONResp(req, &ResponseText{})
	return extra.Error
}
