// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secretscan

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	secretscan_model "forgejo.org/models/secretscan"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	"forgejo.org/modules/process"
	"forgejo.org/modules/secretdetection"
	"forgejo.org/modules/setting"
)

// Codes stored in secret_scan.error: the failures that keep the earlier tips, and the budgets that end a scan as
// incomplete. Never text from git.
const (
	errGit        = "git_error"
	errPanic      = "scan_panic"
	errTooManyRef = "too_many_refs"
	errStore      = "store_failed"

	stopTimeout  = "timeout"
	stopBlobs    = "max_scan_blobs"
	stopBytes    = "max_scan_bytes"
	stopAlerts   = "max_alerts"
	stopFindings = "too_many_findings"
	stopPartial  = "partly_scanned" // a blob the detector could not scan whole
)

const (
	// maxTips is the most refs (distinct tips) of a repository a scan reads; the tips are stored as one text
	maxTips = 100_000
	// maxFindingBlobs is the most files with findings one scan keeps in memory while it looks for their commits
	maxFindingBlobs = 100_000
)

var (
	errDisabled   = errors.New("secret scanning is off")
	errOwnerMoved = errors.New("the repository changed owner while it was scanned")
	errAborted    = errors.New("scan aborted")

	// scanContent runs the detector and says whether it scanned all of the content (false: cut at the detector's size
	// limit, or a rule ran out of its budget). It computes the keyed fingerprints, so it panics before the keying is
	// set up. A test replaces it. The engine reports completeness from its next version on; until then it is true.
	scanContent = func(d *secretdetection.Detector, content []byte) (findings []secretdetection.Finding, complete bool) {
		return d.Scan(content), true
	}

	// afterTips runs when the tips of a scan are read, before any object is (tests hold a scan here)
	afterTips = func(repoID int64, kind string) {}
)

// acquireSlot waits for one of the slots (MAX_CONCURRENT_SCANS scans at once in this process); release gives it
// back. The limit is read at every attempt: a lower one lets the running scans finish, a higher one admits a
// waiting scan within a second.
func acquireSlot(ctx context.Context) (release func(), err error) {
	for {
		limit := setting.SecretScanningSettings(ctx).MaxConcurrentScans // outside the lock: it may query the database
		scanSlots.Lock()
		if int64(scanSlots.running) < max(limit, 1) {
			scanSlots.running++
			scanSlots.Unlock()
			var once sync.Once
			return func() { once.Do(releaseSlot) }, nil
		}
		if scanSlots.freed == nil {
			scanSlots.freed = make(chan struct{})
		}
		freed := scanSlots.freed
		scanSlots.Unlock()
		timer := time.NewTimer(time.Second)
		select {
		case <-freed:
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
		timer.Stop()
	}
}

func releaseSlot() {
	scanSlots.Lock()
	defer scanSlots.Unlock()
	scanSlots.running--
	if scanSlots.freed != nil {
		close(scanSlots.freed)
		scanSlots.freed = nil
	}
}

// Scan reads the objects of the repository (and of its wiki) that the stored scan state doesn't cover and stores the
// alerts and the new state. A scan that fails keeps the earlier state and records an error code; a scan that is
// overtaken (a push meanwhile, a transfer, another process) is queued again.
func Scan(ctx context.Context, repoID int64) error {
	requeue, err := scan(ctx, repoID)
	// after the scan's lock and slot are released: with immediate queues this runs at once
	if requeue {
		Enqueue(repoID)
	}
	return err
}

func scan(ctx context.Context, repoID int64) (requeue bool, err error) {
	if !SiteEnabled(ctx) {
		return false, nil
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if repo_model.IsErrRepoNotExist(err) {
		return false, nil // deleted or trashed since it was queued
	}
	if err != nil {
		return false, err
	}
	if !Enabled(ctx, repo) || repo.Status != repo_model.RepositoryReady {
		return false, nil
	}
	// one scan of a repository at a time, and at most MAX_CONCURRENT_SCANS of all, counted only while one runs
	unlock := lockRepoScan(repo.ID)
	defer unlock()
	release, err := acquireSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()

	cfg := setting.SecretScanningSettings(ctx)
	rules := []secretdetection.Rule{}
	for _, r := range secretdetection.BuiltinRules() {
		if !r.DefaultOff || cfg.GenericRules {
			rules = append(rules, r)
		}
	}
	detector, err := secretdetection.NewDetector(rules)
	if err != nil {
		return false, err
	}
	detector.MaxContentSize = int(cfg.MaxBlobSize)

	scanCtx, cancel, finished := process.GetManager().AddContextTimeout(ctx, cfg.ScanTimeout, fmt.Sprintf("secret scan of repository %d", repo.ID))
	defer finished()
	defer cancel()

	s := &scanner{ctx: ctx, scanCtx: scanCtx, repo: repo, cfg: cfg, detector: detector, rules: map[string]secretdetection.Rule{}}
	for _, r := range rules {
		s.rules[r.ID] = r
	}
	kinds := []string{secretscan_model.KindWiki}
	if !repo.IsEmpty {
		kinds = []string{secretscan_model.KindRepo, secretscan_model.KindWiki}
	}
	for _, kind := range kinds {
		if kind == secretscan_model.KindWiki && !repo.HasWiki() {
			continue
		}
		rq, kerr := s.scanKind(kind)
		requeue = requeue || rq
		err = errors.Join(err, kerr)
	}
	return requeue, err
}

// scanner is the state of one scan of a repository, for the repository and then the wiki.
type scanner struct {
	ctx, scanCtx context.Context
	repo         *repo_model.Repository
	cfg          setting.SecretScanningOptions
	detector     *secretdetection.Detector
	rules        map[string]secretdetection.Rule
}

// blobFinding is a finding with its fingerprint: the secret itself is gone.
type blobFinding struct {
	ruleID, fingerprint string
	start, end          int
}

// foundBlob is a file with findings and the places git says it is at.
type foundBlob struct {
	name     string // the name rev-list gave, for a blob that no commit of the range brings
	findings []blobFinding
	locs     map[string]*change // by path, the oldest commit of the range
	extra    int                // places over the cap, counted
}

// run is one pass over the objects of one git repository.
type run struct {
	*scanner
	kind         string
	blobs, bytes int64
	skipped      int64
	partial      int64  // blobs scanned only in part
	stop         string // the budget that ended the pass
	found        map[string]*foundBlob
	keys         map[string]struct{} // distinct (rule, fingerprint)
	buf          []byte
}

// candidate is an alert to store: a secret and the places it was found at in this scan.
type candidate struct {
	ruleID, fingerprint string
	locs                map[string]*secretscan_model.Location // by path hash and line
	extra               int
}

// outcome is what a scan of one git repository leaves: the new state row and the alerts to store.
type outcome struct {
	state secretscan_model.Scan
	cands []*candidate
}

func (s *scanner) scanKind(kind string) (requeue bool, err error) {
	repoID := s.repo.ID
	old, err := secretscan_model.GetScan(s.ctx, repoID, kind)
	if err != nil {
		return false, err
	}
	open := gitrepo.OpenRepository
	if kind == secretscan_model.KindWiki {
		open = gitrepo.OpenWikiRepository
	}
	gitRepo, err := open(s.scanCtx, s.repo)
	if err != nil {
		return s.fail(kind, old, errGit)
	}
	defer gitRepo.Close()

	tips, err := currentTips(s.scanCtx, gitRepo.Path)
	if err != nil {
		return s.failOr(kind, old, errGit)
	}
	if len(tips) > maxTips {
		return s.fail(kind, old, errTooManyRef)
	}
	var scanned []string
	if old != nil && old.ScannedTips != "" {
		_ = json.Unmarshal([]byte(old.ScannedTips), &scanned) // a state that can't be read scans everything again
	}
	if old != nil && old.Status != secretscan_model.StatusFailed && slices.Equal(tips, scanned) {
		return false, nil // nothing new, e.g. a push that only moved a ref to a commit that is scanned
	}
	afterTips(repoID, kind)
	if scanned, err = existingTips(s.scanCtx, gitRepo, scanned); err != nil {
		return s.failOr(kind, old, errGit)
	}

	r := &run{scanner: s, kind: kind, found: map[string]*foundBlob{}, keys: map[string]struct{}{}}
	if len(tips) > 0 { // rev-list without a revision is a usage error
		if err := r.enumerate(gitRepo, tips, scanned); err != nil {
			return s.failOr(kind, old, errorCode(err))
		}
	}
	if len(r.found) > 0 {
		if err := r.attribute(gitRepo, tips, scanned); err != nil {
			return s.failOr(kind, old, errorCode(err))
		}
	}
	if s.ctx.Err() != nil {
		return false, s.ctx.Err() // shutting down: nothing is stored, the next start scans again
	}
	if s.scanCtx.Err() != nil && r.stop == "" {
		r.stop = stopTimeout
	}
	tipsJSON, err := json.Marshal(tips)
	if err != nil {
		return false, err
	}
	out := &outcome{state: secretscan_model.Scan{
		RepoID: repoID, Kind: kind, ScannedTips: string(tipsJSON), Status: secretscan_model.StatusOK,
		Blobs: r.blobs, Bytes: r.bytes, SkippedLarge: r.skipped,
	}, cands: r.candidates()}
	if r.stop == "" && r.partial > 0 {
		r.stop = stopPartial // counts like a budget: the tips are stored and the cron doesn't retry
	}
	if r.stop != "" {
		out.state.Status, out.state.Error = secretscan_model.StatusIncomplete, r.stop
	}
	return s.store(old, out)
}

// errorCode is the code stored for a failed pass: a panic, or git.
func errorCode(err error) string {
	if errors.Is(err, errAborted) {
		return errPanic
	}
	return errGit
}

// enumerate reads every blob of the range once and keeps the findings. A budget that is spent ends it with what
// it has; r.stop says which.
func (r *run) enumerate(gitRepo *git.Repository, tips, scanned []string) error {
	return gitRepo.WithCatFileBatchCheck(r.scanCtx, func(cw io.Writer, cr *bufio.Reader) error {
		return gitRepo.WithCatFileBatch(r.scanCtx, func(bw io.Writer, br *bufio.Reader) error {
			err := listObjects(r.scanCtx, gitRepo.Path, rangeInput(tips, scanned), func(id, name []byte) error {
				if r.stop != "" || r.scanCtx.Err() != nil {
					return errStopped
				}
				return r.object(cw, cr, bw, br, string(id), string(name))
			})
			if r.stop != "" || r.scanCtx.Err() != nil {
				return nil // the budget, not git, ended the pass; every request was answered
			}
			return err
		})
	})
}

var errStopped = errors.New("stopped")

// object checks the type and size of one object, and reads and scans it when it is a blob within the budgets.
func (r *run) object(cw io.Writer, cr *bufio.Reader, bw io.Writer, br *bufio.Reader, id, name string) error {
	if _, err := io.WriteString(cw, id+"\n"); err != nil {
		return err
	}
	_, typ, size, err := git.ReadBatchLine(cr)
	if git.IsErrNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if typ != "blob" {
		return nil
	}
	if size > r.cfg.MaxBlobSize {
		r.skipped++
		return nil // never read
	}
	if r.blobs+1 > r.cfg.MaxScanBlobs {
		r.stop = stopBlobs
		return errStopped
	}
	if r.bytes+size > r.cfg.MaxScanBytes {
		r.stop = stopBytes
		return errStopped
	}
	if _, err := io.WriteString(bw, id+"\n"); err != nil {
		return err
	}
	_, typ, size, err = git.ReadBatchLine(br)
	if err != nil {
		return err
	}
	if cap(r.buf) < int(size) {
		r.buf = make([]byte, size)
	}
	buf := r.buf[:size]
	if _, err := io.ReadFull(br, buf); err != nil {
		return err
	}
	if _, err := br.Discard(1); err != nil { // the newline after the content
		return err
	}
	r.blobs++
	r.bytes += size
	if typ != "blob" {
		return nil
	}
	return r.scanBlob(id, name, buf)
}

// scanBlob runs the detector over one blob. A panic (in a rule, or the fingerprint before the keying is set up)
// is recovered here, so the process survives and the log names the blob, and it fails the scan: skipping the blob
// would leave a hole in what the stored state calls scanned.
func (r *run) scanBlob(id, name string, content []byte) (err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Error("secret scanning: panic (%T) scanning blob %s of repository %d", p, id, r.repo.ID)
			err = errAborted
		}
	}()
	findings, complete := scanContent(r.detector, content)
	if !complete {
		r.partial++
	}
	if len(findings) == 0 {
		return nil
	}
	fb := &foundBlob{name: name, locs: map[string]*change{}}
	for _, f := range findings {
		bf := blobFinding{ruleID: f.RuleID, fingerprint: f.Fingerprint(), start: f.StartLine, end: f.EndLine}
		if _, seen := r.keys[bf.ruleID+"|"+bf.fingerprint]; !seen {
			if int64(len(r.keys)) >= r.cfg.MaxAlertsPerRepo {
				r.stop = stopAlerts
				return errStopped
			}
			r.keys[bf.ruleID+"|"+bf.fingerprint] = struct{}{}
		}
		fb.findings = append(fb.findings, bf)
	}
	if len(r.found) >= maxFindingBlobs {
		r.stop = stopFindings
		return errStopped
	}
	r.found[id] = fb
	return nil
}

// attribute asks git for the commit, path and author of every blob with findings, over the same range.
func (r *run) attribute(gitRepo *git.Repository, tips, scanned []string) error {
	err := listChanges(r.scanCtx, gitRepo.Path, rangeInput(tips, scanned), func(c change) {
		fb := r.found[c.NewID]
		if fb == nil {
			return
		}
		if old, ok := fb.locs[c.Path]; ok {
			if c.CommitUnix < old.CommitUnix { // log is newest first: the oldest commit of the range brought it
				*old = c
			}
			return
		}
		if int64(len(fb.locs)) >= r.cfg.MaxLocationsPerAlert {
			fb.extra++
			return
		}
		fb.locs[c.Path] = &c
	})
	if r.scanCtx.Err() != nil {
		return nil // the time budget ended it; what was read is kept
	}
	return err
}

// candidates turns the findings of the blobs into alerts with their places.
func (r *run) candidates() []*candidate {
	byKey := map[string]*candidate{}
	for _, id := range slices.Sorted(maps.Keys(r.found)) {
		fb := r.found[id]
		places := []*change{}
		for _, c := range fb.locs {
			places = append(places, c)
		}
		if len(places) == 0 && fb.extra == 0 {
			places = append(places, &change{Path: fb.name}) // only a ref reaches it: no commit
		}
		for _, f := range fb.findings {
			c := byKey[f.ruleID+"|"+f.fingerprint]
			if c == nil {
				c = &candidate{ruleID: f.ruleID, fingerprint: f.fingerprint, locs: map[string]*secretscan_model.Location{}}
				byKey[f.ruleID+"|"+f.fingerprint] = c
			}
			c.extra += fb.extra
			for _, p := range places {
				loc := secretscan_model.NewLocation(r.repo.ID, r.kind, id, p.Path, f.start, f.end, p.Commit, p.AuthorName, p.AuthorEmail, p.CommitUnix)
				key := fmt.Sprintf("%s:%d", loc.PathHash, loc.StartLine)
				if have := c.locs[key]; have == nil || older(loc, have) {
					c.locs[key] = loc
				}
			}
		}
	}
	return slices.SortedFunc(maps.Values(byKey), func(a, b *candidate) int {
		return cmp.Or(cmp.Compare(a.ruleID, b.ruleID), cmp.Compare(a.fingerprint, b.fingerprint))
	})
}

// older tells whether a place is a better answer to "where did it come from" than have: one with a commit beats one
// without, then the older commit, then the lower blob ID (a fixed order, so repeated scans agree).
func older(loc, have *secretscan_model.Location) bool {
	switch {
	case have.CommitID == "":
		return loc.CommitID != ""
	case loc.CommitID == "":
		return false
	}
	return cmp.Or(cmp.Compare(loc.CommitUnix, have.CommitUnix), cmp.Compare(loc.BlobID, have.BlobID)) < 0
}

// fail records the error code of a scan and keeps the earlier tips.
func (s *scanner) fail(kind string, old *secretscan_model.Scan, code string) (requeue bool, err error) {
	log.Warn("secret scanning: scan of repository %d (%s) failed: %s", s.repo.ID, kind, code)
	st := secretscan_model.Scan{RepoID: s.repo.ID, Kind: kind, Status: secretscan_model.StatusFailed, Error: code}
	if old != nil {
		st.ScannedTips, st.Blobs, st.Bytes, st.SkippedLarge = old.ScannedTips, old.Blobs, old.Bytes, old.SkippedLarge
	}
	return s.store(old, &outcome{state: st})
}

// failOr is fail, except when the process is shutting down: that is not a failure of the repository.
func (s *scanner) failOr(kind string, old *secretscan_model.Scan, code string) (bool, error) {
	if s.ctx.Err() != nil {
		return false, s.ctx.Err()
	}
	return s.fail(kind, old, code)
}

// store writes the alerts and the state row in one transaction, after the last check of the switches. The write is
// discarded when secret scanning was turned off or the repository trashed, and the scan is queued again when the
// repository changed owner (the new owner's switch and rules decide) or another process wrote the state first.
func (s *scanner) store(old *secretscan_model.Scan, out *outcome) (requeue bool, err error) {
	cfg := setting.SecretScanningSettings(s.ctx) // before the transaction: it may query the database
	err = db.WithTx(s.ctx, func(ctx context.Context) error {
		enabled, owner, err := enabledStored(ctx, s.repo.ID)
		if err != nil {
			return err
		}
		if !enabled {
			return errDisabled
		}
		if owner != s.repo.OwnerID {
			return errOwnerMoved
		}
		if out.state.Status != secretscan_model.StatusFailed {
			capped, err := writeAlerts(ctx, cfg, s.repo.ID, out.state.Kind, out.cands)
			if err != nil {
				return err
			}
			if capped && out.state.Status == secretscan_model.StatusOK {
				out.state.Status, out.state.Error = secretscan_model.StatusIncomplete, stopAlerts
			}
		}
		return secretscan_model.Save(ctx, old, &out.state)
	})
	switch {
	case err == nil, errors.Is(err, errDisabled):
		return false, nil
	case errors.Is(err, errOwnerMoved), errors.Is(err, secretscan_model.ErrScanMoved):
		return true, nil
	}
	// a write that lost a race on a unique key looks like any error: if the state row moved, the scan is overtaken
	if cur, rerr := secretscan_model.GetScan(s.ctx, s.repo.ID, out.state.Kind); rerr == nil && (cur == nil) != (old == nil) || cur != nil && old != nil && cur.Version != old.Version {
		return true, nil
	}
	log.Error("secret scanning: store the scan of repository %d (%s): %v", s.repo.ID, out.state.Kind, err)
	return false, errors.New(errStore)
}

// writeAlerts stores the alerts and their places, relying on the unique keys: an alert or a place that is already
// there is not stored again. Places over MAX_LOCATIONS_PER_ALERT are only counted, and alerts over MAX_ALERTS_PER_REPO
// are left out (capped).
func writeAlerts(ctx context.Context, cfg setting.SecretScanningOptions, repoID int64, kind string, cands []*candidate) (capped bool, err error) {
	total, err := secretscan_model.CountAlerts(ctx, repoID)
	if err != nil {
		return false, err
	}
	for _, c := range cands {
		a, err := secretscan_model.GetAlertByKey(ctx, repoID, c.ruleID, c.fingerprint)
		if err != nil {
			return false, err
		}
		if a == nil {
			if total >= cfg.MaxAlertsPerRepo {
				capped = true
				continue
			}
			rule, _ := secretdetection.RuleByID(c.ruleID)
			a = &secretscan_model.Alert{RepoID: repoID, RuleID: c.ruleID, SecretType: rule.Type, Fingerprint: c.fingerprint}
			if err := secretscan_model.InsertAlert(ctx, a); err != nil {
				return false, err
			}
			total++
		}
		stored, err := secretscan_model.CountLocations(ctx, a.ID)
		if err != nil {
			return false, err
		}
		seen := c.extra
		for _, key := range slices.Sorted(maps.Keys(c.locs)) {
			loc := c.locs[key]
			has, err := secretscan_model.HasLocation(ctx, a.ID, kind, loc.PathHash, loc.StartLine)
			if err != nil {
				return false, err
			}
			if has {
				continue
			}
			seen++
			if stored >= cfg.MaxLocationsPerAlert {
				continue
			}
			loc.AlertID = a.ID
			if _, err := db.GetEngine(ctx).Insert(loc); err != nil {
				return false, err
			}
			stored++
		}
		if seen > 0 {
			if err := secretscan_model.AddLocationCount(ctx, a.ID, seen); err != nil {
				return false, err
			}
		}
	}
	return capped, nil
}
