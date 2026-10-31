// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package structs

import "time"

// SecretScanningLocation is a place a secret was found: a file at a line, in the commit that brought it. Path and
// the commit author are text from git, not checked by spoved: clients must escape them.
// swagger:model
type SecretScanningLocation struct {
	// repo (the repository) or wiki
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	CommitSHA string `json:"commit_sha"`
	BlobSHA   string `json:"blob_sha"`
	// the author of the commit, as it names itself (anyone can write any name)
	CommitAuthorName  string `json:"commit_author_name"`
	CommitAuthorEmail string `json:"commit_author_email"`
	// swagger:strfmt date-time
	CommitDate *time.Time `json:"commit_date,omitempty"`
	// the file at that commit (the page, for the wiki); empty when the place has no commit
	HTMLURL string `json:"html_url"`
}

// SecretScanningAlert is a secret found in the code or wiki of a repository. It never holds the secret.
// swagger:model
type SecretScanningAlert struct {
	Number int64 `json:"number"`
	// open, revoked, false_positive or used_in_tests
	State string `json:"state"`
	// the reason the alert was closed with: the same as its closed state, empty while open
	Resolution        string `json:"resolution"`
	ResolutionComment string `json:"resolution_comment"`
	ResolvedBy        *User  `json:"resolved_by"`
	// swagger:strfmt date-time
	ResolvedAt *time.Time `json:"resolved_at"`
	RuleID     string     `json:"rule_id"`
	// the group of the rule (aws, github, ...)
	SecretType            string `json:"secret_type"`
	SecretTypeDisplayName string `json:"secret_type_display_name"`
	// the keyed fingerprint of the secret: the same secret has the same one in every location
	Fingerprint string `json:"fingerprint"`
	// swagger:strfmt date-time
	CreatedAt time.Time `json:"created_at"`
	// swagger:strfmt date-time
	UpdatedAt time.Time `json:"updated_at"`
	HTMLURL   string    `json:"html_url"`
	// every place seen, also those over the stored cap
	LocationsCount int `json:"locations_count"`
	// the stored places (only when one alert is read; at most 100)
	Locations []*SecretScanningLocation `json:"locations,omitempty"`
}

// EditSecretScanningAlertOption closes or reopens an alert.
// swagger:model
type EditSecretScanningAlertOption struct {
	// open to reopen, or the closed state: revoked, false_positive or used_in_tests
	// required: true
	State string `json:"state"`
	// optional, at most 1000 bytes; refused when it looks like a secret
	Comment string `json:"comment"`
}
