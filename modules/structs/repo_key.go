// Copyright 2015 The Gogs Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package structs

import (
	"time"
)

// DeployKey a deploy key
type DeployKey struct {
	ID          int64  `json:"id"`
	KeyID       int64  `json:"key_id"`
	Key         string `json:"key"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Fingerprint string `json:"fingerprint"`
	// swagger:strfmt date-time
	Created    time.Time   `json:"created_at"`
	ReadOnly   bool        `json:"read_only"`
	Repository *Repository `json:"repository,omitempty"`
	// When the key expires, omitted if never
	// swagger:strfmt date-time
	Expires *time.Time `json:"expires_at,omitempty"`
}

// CreateKeyOption options when creating a key
type CreateKeyOption struct {
	// Title of the key to add
	//
	// required: true
	// unique: true
	Title string `json:"title" binding:"Required"`
	// An armored SSH key to add
	//
	// required: true
	// unique: true
	Key string `json:"key" binding:"Required"`
	// Describe if the key has only read access or read/write
	//
	// required: false
	ReadOnly bool `json:"read_only"`
	// When it expires. Required by default, at most 365 days from today ([security] *_EXPIRY_REQUIRED and *_MAX_LIFETIME_DAYS); omit for never only when the instance allows it
	// swagger:strfmt date-time
	ExpiresAt *time.Time `json:"expires_at"`
}
