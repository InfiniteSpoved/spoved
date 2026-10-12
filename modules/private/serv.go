// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	asymkey_model "forgejo.org/models/asymkey"
	"forgejo.org/models/perm"
	user_model "forgejo.org/models/user"
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
	IsWiki         bool
	DeployKeyKeyID int64  // public key ID if the key is a deploy key
	KeyID          int64  // public key
	KeyName        string // this field is ambiguous, it can be the name of DeployKey, or the name of the PublicKey
	UserName       string
	UserEmail      string
	UserID         int64
	OwnerName      string
	RepoName       string
	RepoID         int64
	ExpiresUnix    timeutil.TimeStamp // expiry of the key (deploy key or public key) used, 0 = never
}

// ServCommand preps for a serv call
// clientIP is the SSH client address, for the IP allowlist.
func ServCommand(ctx context.Context, keyID int64, ownerName, repoName string, mode perm.AccessMode, clientIP string, verbs ...string) (*ServCommandResults, ResponseExtra) {
	var reqURL strings.Builder
	fmt.Fprintf(&reqURL, "/api/internal/serv/command/%d/%s/%s?mode=%d&ip=%s",
		keyID,
		url.PathEscape(ownerName),
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
