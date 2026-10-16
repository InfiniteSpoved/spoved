// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repository

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	audit_model "forgejo.org/models/audit"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
)

// env keys for git hooks need
const (
	EnvRepoName          = "GITEA_REPO_NAME"
	EnvRepoUsername      = "GITEA_REPO_USER_NAME"
	EnvRepoID            = "GITEA_REPO_ID"
	EnvRepoIsWiki        = "GITEA_REPO_IS_WIKI"
	EnvPusherName        = "GITEA_PUSHER_NAME"
	EnvPusherEmail       = "GITEA_PUSHER_EMAIL"
	EnvPusherID          = "GITEA_PUSHER_ID"
	EnvKeyID             = "GITEA_KEY_ID"               // public key ID
	EnvDeployPublicKeyID = "GITEA_DEPLOY_PUBLIC_KEY_ID" // public key ID of the deploy key in use
	EnvPRID              = "GITEA_PR_ID"
	EnvPushTrigger       = "GITEA_PUSH_TRIGGER"
	EnvIsInternal        = "GITEA_INTERNAL_PUSH"
	EnvAppURL            = "GITEA_ROOT_URL"
	EnvActionTaskID      = "GITEA_ACTION_TASK_ID"
	EnvAuditCredential   = "GITEA_AUDIT_CREDENTIAL" // <type>:<id> of the credential that pushes, checked again by the hook
	EnvAuditIP           = "GITEA_AUDIT_IP"
	EnvAuditRequestID    = "GITEA_AUDIT_REQUEST_ID"
	EnvAuditSudoBy       = "GITEA_AUDIT_SUDO_BY" // ID of the admin who pushes with API sudo, "" otherwise; checked again by the hook
)

type PushTrigger string

const (
	PushTriggerPRMergeToBase    PushTrigger = "pr-merge-to-base"
	PushTriggerPRUpdateWithBase PushTrigger = "pr-update-with-base"
	PushTriggerWeb              PushTrigger = "web" // any other push the server makes, e.g. the web editor
)

// AuditEnv is the hook environment that tells the audit log who pushes. All four keys are always set, and callers
// append them after the inherited environment, so that a value inherited from the server's own environment never
// wins.
func AuditEnv(credential, ip, requestID, sudoBy string) []string {
	return []string{EnvAuditCredential + "=" + credential, EnvAuditIP + "=" + ip, EnvAuditRequestID + "=" + requestID, EnvAuditSudoBy + "=" + sudoBy}
}

// AuditEnvironment is AuditEnv for the audit actor of ctx; empty values without one.
func AuditEnvironment(ctx context.Context) []string {
	a := audit_model.ActorFromContext(ctx)
	if a == nil {
		return AuditEnv("", "", "", "")
	}
	sudoBy := ""
	if a.SudoByID != 0 {
		sudoBy = strconv.FormatInt(a.SudoByID, 10)
	}
	return AuditEnv(a.Credential.Type+":"+strconv.FormatInt(a.Credential.ID, 10), a.IP, a.RequestID, sudoBy)
}

// InternalPushingEnvironment returns an os environment to switch off hooks on push
// It is recommended to avoid using this unless you are pushing within a transaction
// or if you absolutely are sure that post-receive and pre-receive will do nothing
// We provide the full pushing-environment for other hook providers
func InternalPushingEnvironment(ctx context.Context, doer *user_model.User, repo *repo_model.Repository) []string {
	return append(PushingEnvironment(ctx, doer, repo),
		EnvIsInternal+"=true",
	)
}

// PushingEnvironment returns an os environment to allow hooks to work on push
func PushingEnvironment(ctx context.Context, doer *user_model.User, repo *repo_model.Repository) []string {
	return FullPushingEnvironment(ctx, doer, doer, repo, repo.Name, 0)
}

// FullPushingEnvironment returns an os environment to allow hooks to work on push
func FullPushingEnvironment(ctx context.Context, author, committer *user_model.User, repo *repo_model.Repository, repoName string, prID int64) []string {
	isWiki := "false"
	if strings.HasSuffix(repoName, ".wiki") {
		isWiki = "true"
	}

	authorSig := author.NewGitSig()
	committerSig := committer.NewGitSig()

	environ := append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorSig.Name,
		"GIT_AUTHOR_EMAIL="+authorSig.Email,
		"GIT_COMMITTER_NAME="+committerSig.Name,
		"GIT_COMMITTER_EMAIL="+committerSig.Email,
		EnvRepoName+"="+repoName,
		EnvRepoUsername+"="+repo.OwnerName,
		EnvRepoIsWiki+"="+isWiki,
		EnvPusherName+"="+committer.Name,
		EnvPusherID+"="+fmt.Sprintf("%d", committer.ID),
		EnvRepoID+"="+fmt.Sprintf("%d", repo.ID),
		EnvPRID+"="+fmt.Sprintf("%d", prID),
		EnvAppURL+"="+setting.AppURL,
		"SSH_ORIGINAL_COMMAND=gitea-internal",
		EnvPushTrigger+"="+string(PushTriggerWeb), // a merge appends its own
	)
	environ = append(environ, AuditEnvironment(ctx)...)

	if !committer.KeepEmailPrivate {
		environ = append(environ, EnvPusherEmail+"="+committer.Email)
	}

	return environ
}
