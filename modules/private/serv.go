// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/perm"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/json"
	"forgejo.org/modules/timeutil"
)

// KeyAndOwner is the response from ServNoCommand
type KeyAndOwner struct {
	Key   *asymkey_model.PublicKey `json:"key"`
	Owner *user_model.User         `json:"user"`
}

// ServNoCommand returns information about the provided key
func ServNoCommand(ctx context.Context, keyID int64) (*asymkey_model.PublicKey, *user_model.User, error) {
	reqURL := fmt.Sprintf("/api/internal/serv/none/%d", keyID)
	req := newInternalRequest(ctx, reqURL, "GET")
	keyAndOwner, extra := requestJSONResp(req, &KeyAndOwner{})
	if extra.HasError() {
		return nil, nil, extra.Error
	}
	return keyAndOwner.Key, keyAndOwner.Owner, nil
}

// ServCommandResults are the results of a call to the private route serv
type ServCommandResults struct {
	IsWiki          bool
	DeployKeyKeyID  int64  // public key ID if the key is a deploy key
	KeyID           int64  // public key
	KeyName         string // this field is ambiguous, it can be the name of DeployKey, or the name of the PublicKey
	UserName        string
	UserEmail       string
	UserID          int64
	OwnerName       string
	GroupName       string // the project of a repository in one, from the database
	RepoName        string
	RepoID          int64
	ExpiresUnix     timeutil.TimeStamp // expiry of the key (deploy key or public key) used, 0 = never
	AuditCredential string             // "type:id" of the credential, for the LFS token's cred claim
	RequestID       string             // of the audit entries of this command, passed on to the hooks
}

// ServCommand preps for a serv call
// repoName is `repo` or, for a repository in a project, `project/repo`.
// clientIP is the SSH client address, for the IP allowlist.
func ServCommand(ctx context.Context, keyID int64, ownerName, repoName string, mode perm.AccessMode, clientIP string, verbs ...string) (*ServCommandResults, ResponseExtra) {
	var reqURL strings.Builder
	group, repoName, _ := strings.Cut(repoName, "/")
	if repoName == "" {
		group, repoName = "", group
	} else {
		group = url.PathEscape(group) + "/"
	}
	fmt.Fprintf(&reqURL, "/api/internal/serv/command/%d/%s/%s%s?mode=%d&ip=%s",
		keyID,
		url.PathEscape(ownerName),
		group,
		url.PathEscape(repoName),
		mode,
		url.QueryEscape(clientIP))
	for _, verb := range verbs {
		if verb != "" {
			fmt.Fprintf(&reqURL, "&verb=%s", url.QueryEscape(verb))
		}
	}
	req := newInternalRequest(ctx, reqURL.String(), "GET")
	return requestJSONResp(req, &ServCommandResults{})
}

// ServOperationSlot waits until the server gives this process one of its git operation slots, and holds it until
// release is called (or the process exits). A busy server answers with status 503 and a message for the user.
func ServOperationSlot(ctx context.Context) (release func(), extra ResponseExtra) {
	ctx, cancel := context.WithCancel(ctx)
	req := newInternalRequest(ctx, "/api/internal/serv/slot", "GET").SetTimeout(10*time.Second, 0)
	resp, err := req.Response()
	if err != nil {
		cancel()
		extra.UserMsg = "Internal Server Connection Error"
		extra.Error = fmt.Errorf("unable to contact gitea %q: %w", req.GoString(), err)
		return nil, extra
	}
	if resp.StatusCode != http.StatusOK {
		defer cancel()
		defer resp.Body.Close()
		var respErr Response
		_ = json.NewDecoder(resp.Body).Decode(&respErr)
		extra.StatusCode = resp.StatusCode
		extra.UserMsg = respErr.UserMsg
		if extra.UserMsg == "" {
			extra.UserMsg = "Internal Server Error (no message for end users)"
		}
		extra.Error = responseError{statusCode: resp.StatusCode, errorString: respErr.Err}
		return nil, extra
	}
	// the server keeps the slot until this connection closes
	return func() {
		resp.Body.Close()
		cancel()
	}, extra
}
