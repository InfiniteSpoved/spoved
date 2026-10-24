// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import (
	"time"
)

// AccessToken represents an API access token
// swagger:response AccessToken
type AccessToken struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Token          string    `json:"sha1"`
	TokenLastEight string    `json:"token_last_eight"`
	Scopes         []string  `json:"scopes"`
	Created        time.Time `json:"created_at"`
	// Indicates that an access token only has access to the specified repositories.  Will be null if the access token
	// is not limited to a set of specified repositories.
	Repositories []*RepositoryMeta `json:"repositories"`
	// When the token expires, omitted if never
	// swagger:strfmt date-time
	Expires *time.Time `json:"expires_at,omitempty"`
	// The permission of a token of an organization, a project or a repository on the repositories it covers: read,
	// write or admin. Omitted for a personal access token.
	Permission string `json:"permission,omitempty"`
	// The login of who created a token of an organization, a project or a repository. Omitted for a personal access
	// token.
	CreatedBy string `json:"created_by,omitempty"`
	// What a token of an organization, a project or a repository is for, if its creator said
	Description string `json:"description,omitempty"`
	// Whether a token of an organization, a project or a repository may rotate itself (POST /tokens/self/rotate)
	SelfRotate bool `json:"self_rotate,omitempty"`
	// When a token of an organization, a project or a repository was last used, omitted if never
	// swagger:strfmt date-time
	LastUsed *time.Time `json:"last_used_at,omitempty"`
	// The client address of that use, omitted when unknown
	LastUsedIP string `json:"last_used_ip,omitempty"`
	// When a token of an organization, a project or a repository was last rotated, omitted if never
	// swagger:strfmt date-time
	Rotated *time.Time `json:"rotated_at,omitempty"`
	// The login of who rotated it last, omitted when it rotated itself (rotated_by_self) or never was
	RotatedBy string `json:"rotated_by,omitempty"`
	// Whether it rotated itself last
	RotatedBySelf bool `json:"rotated_by_self,omitempty"`
}

// AccessTokenList represents a list of API access token.
type AccessTokenList []*AccessToken

// CreateAccessTokenOption options when create access token
// swagger:model CreateAccessTokenOption
type CreateAccessTokenOption struct {
	// required: true
	Name string `json:"name" binding:"Required"`
	// example: ["all", "read:activitypub","read:issue", "write:misc", "read:notification", "read:organization", "read:package", "read:repository", "read:user"]
	Scopes []string `json:"scopes"`
	// If provided and not-empty, creates an access token with access only to specified repositories.
	Repositories []*RepoTargetOption `json:"repositories"`
	// When it expires. Required by default, at most 365 days from today ([security] *_EXPIRY_REQUIRED and *_MAX_LIFETIME_DAYS); omit for never only when the instance allows it
	// swagger:strfmt date-time
	ExpiresAt *time.Time `json:"expires_at"`
}

// CreateOwnedAccessTokenOption options to create an access token of an organization, a project or a repository
// swagger:model CreateOwnedAccessTokenOption
type CreateOwnedAccessTokenOption struct {
	// required: true
	Name string `json:"name" binding:"Required;MaxSize(255)"`
	// At least one of read:repository, write:repository, read:issue, write:issue, read:package, write:package and
	// read:organization
	// required: true
	// example: ["write:repository"]
	Scopes []string `json:"scopes"`
	// What the token may do on the repositories it covers; the lower of this and its scopes applies
	// required: true
	// enum: read,write,admin
	Permission string `json:"permission" binding:"Required;In(read,write,admin)"`
	// Organization tokens only: the repositories it covers, as "repo" or "project/repo". Omitted or null covers
	// every repository of the organization, also ones created later.
	Repositories []string `json:"repositories"`
	// When it expires. Required by default, at most 365 days from today ([security] ACCESS_TOKEN_EXPIRY_REQUIRED and ACCESS_TOKEN_MAX_LIFETIME_DAYS); omit for never only when the instance allows it
	// swagger:strfmt date-time
	ExpiresAt *time.Time `json:"expires_at"`
	// What the token is for, at most 255 characters
	Description string `json:"description" binding:"MaxSize(255)"`
	// Let the token rotate itself with POST /tokens/self/rotate
	SelfRotate bool `json:"self_rotate"`
}

// RotateOwnedAccessTokenOption options to rotate an access token of an organization, a project or a repository
// swagger:model RotateOwnedAccessTokenOption
type RotateOwnedAccessTokenOption struct {
	// When the new secret expires; omitted gives the default lifetime (30 days, capped at the instance's maximum),
	// or none for a token without an expiry where the instance allows it
	// swagger:strfmt date-time
	ExpiresAt *time.Time `json:"expires_at"`
}

// CreateOAuth2ApplicationOptions holds options to create an oauth2 application
type CreateOAuth2ApplicationOptions struct {
	Name               string   `json:"name" binding:"Required"`
	ConfidentialClient bool     `json:"confidential_client"`
	RedirectURIs       []string `json:"redirect_uris" binding:"Required"`
	// Expiry of the new client secret; required unless the instance's policy allows no expiry
	ClientSecretExpiresAt *time.Time `json:"client_secret_expires_at"`
}

// OAuth2Application represents an OAuth2 application
// swagger:response OAuth2Application
type OAuth2Application struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	ClientID           string    `json:"client_id"`
	ClientSecret       string    `json:"client_secret"`
	ConfidentialClient bool      `json:"confidential_client"`
	RedirectURIs       []string  `json:"redirect_uris"`
	Created            time.Time `json:"created"`
	// absent when the client secret never expires
	ClientSecretExpiresAt *time.Time `json:"client_secret_expires_at,omitempty"`
}

// OAuth2ApplicationList represents a list of OAuth2 applications.
// swagger:response OAuth2ApplicationList
type OAuth2ApplicationList []*OAuth2Application
