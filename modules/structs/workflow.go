// Copyright The Forgejo Authors.
// SPDX-License-Identifier: MIT

package structs

import "time"

// DispatchWorkflowOption options when dispatching a workflow
// swagger:model
type DispatchWorkflowOption struct {
	// Git reference for the workflow
	//
	// required: true
	Ref string `json:"ref"`
	// Input keys and values configured in the workflow file.
	Inputs map[string]string `json:"inputs"`
	// Flag to return the run info
	// default: false
	ReturnRunInfo bool `json:"return_run_info"`
	// Time to start the run (RFC 3339) instead of now; the run is then scheduled and return_run_info is ignored
	RunAt *time.Time `json:"run_at"`
}

// DispatchWorkflowRun represents a workflow run
// swagger:model
type DispatchWorkflowRun struct {
	// the workflow run id
	ID int64 `json:"id"`
	// a unique number for each run of a repository
	RunNumber int64 `json:"run_number"`
	// the jobs name
	Jobs []string `json:"jobs"`
}

// ScheduledWorkflowRun represents a manual workflow run that starts at a chosen time
// swagger:model
type ScheduledWorkflowRun struct {
	// the id of the scheduled run
	ID int64 `json:"id"`
	// the workflow file name
	Workflow string `json:"workflow"`
	// the git reference the run starts from
	Ref string `json:"ref"`
	// the time the run starts
	RunAt time.Time `json:"run_at"`
	// the login name of the user who scheduled it, the run's actor
	Doer string `json:"doer"`
	// "pending", or "cancelled" when the run can no longer start
	Status string `json:"status"`
	// why the run was cancelled, a short code such as "no_permission"
	CancelReason string `json:"cancel_reason"`
	// when the run was scheduled
	Created time.Time `json:"created"`
}
