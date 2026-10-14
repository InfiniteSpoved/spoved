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
