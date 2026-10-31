// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package structs

import "time"

// SecretScanningLocation is a place a secret was found: a file at a line, in the commit that brought it. Path and
// the commit author are text from git, not checked by spoved: clients must escape them.
// swagger:model
type SecretScanningLocation struct {
	// repo (the code of the repository), wiki, or pull (a commit only a refs/pull/* ref reaches)
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

// SecretScanningAlert is a secret found in the code or wiki of a repository. It never holds the secret. The shape
// follows GitHub's secret scanning alerts.
// swagger:model
type SecretScanningAlert struct {
	Number int64 `json:"number"`
	// open or resolved
	State string `json:"state"`
	// why the alert is resolved: false_positive, wont_fix, revoked or used_in_tests; empty while open
	Resolution        string `json:"resolution"`
	ResolutionComment string `json:"resolution_comment"`
	ResolvedBy        *User  `json:"resolved_by"`
	// swagger:strfmt date-time
	ResolvedAt *time.Time `json:"resolved_at"`
	// the rule that found the secret (aws-access-key-id, ...)
	SecretType string `json:"secret_type"`
	// the name of that rule
	SecretTypeDisplayName string `json:"secret_type_display_name"`
	// the keyed fingerprint of the secret: the same secret has the same one in every location
	Fingerprint string `json:"fingerprint"`
	// swagger:strfmt date-time
	CreatedAt time.Time `json:"created_at"`
	// swagger:strfmt date-time
	UpdatedAt time.Time `json:"updated_at"`
	// the page of the alert in the web interface (not there yet)
	HTMLURL string `json:"html_url"`
	// the places of the alert, paged
	LocationsURL string `json:"locations_url"`
	// the places seen, also those over the stored cap; without the wiki places for a viewer who can't read the wiki
	LocationsCount int `json:"locations_count"`
	// true while only refs/pull/* (which any reader can write) reach the secret
	ViaPull bool `json:"via_pull"`
}

// EditSecretScanningAlertOption resolves or reopens an alert.
// swagger:model
type EditSecretScanningAlertOption struct {
	// open to reopen, resolved to resolve
	// required: true
	State string `json:"state"`
	// required when resolving: false_positive, wont_fix, revoked or used_in_tests
	Resolution string `json:"resolution"`
	// optional when resolving, at most 1000 bytes; refused when it looks like a secret
	ResolutionComment string `json:"resolution_comment"`
}

// SecretScanningStatus sums up how far the scan of a repository (its code, its wiki and its pull refs) has come.
// swagger:model
type SecretScanningStatus struct {
	// pending (not scanned yet), ok, incomplete (under way, or with parts that were not scanned) or failed
	Status string `json:"status"`
	// a code (max_scan_bytes, over_limit, max_alerts, timeout, git_error, ...), empty when ok
	Reason string `json:"reason"`
	// files and secrets that were marked done without being scanned
	Holes int64 `json:"holes"`
	// files over the size limit that were not read
	SkippedLarge int64 `json:"skipped_large"`
	// when the last scan that did not fail ended
	// swagger:strfmt date-time
	LastScanned *time.Time `json:"last_scanned"`
}
