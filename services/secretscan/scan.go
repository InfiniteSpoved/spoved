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
	"strconv"
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
	sync_module "forgejo.org/modules/sync"
	"forgejo.org/modules/timeutil"
)

// Codes stored in secret_scan.error: the failures, and the limits that end a slice of a scan. Never text from git.
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
	stopSlice    = "in_progress"    // a slice ended with work left for the next one
)

const (
	// maxTips is the most refs (distinct tips) of a repository a scan reads; the tips are stored as one text
	maxTips = 100_000
	// maxFindingBlobs is the most files with findings one unit keeps in memory while it looks for their commits
	maxFindingBlobs = 100_000
	// maxUnitLocBytes is the most path text one unit keeps for the places of its findings; more places are counted
	maxUnitLocBytes = 32 << 20
	// unitTips is the most tips of one group a unit reads together
	unitTips = 256
	// maxCommits is the most commits listed when a unit that is over the limits is cut into a prefix of its commits
	maxCommits = 1_000_000
	// shrinkAttempts is how often a unit is tried again with fewer commits
	shrinkAttempts = 8
	// alertBatch is the number of alerts looked up and written together
	alertBatch = 200

	// A scan that fails, or whose slice makes no progress, is tried again (by the next trigger, the cron task) only
	// after retryBase, doubling per failure in a row up to retryMax. While a scan is under way the row's retry time
	// is retryBase ahead, so the cron task picks it up if the queue lost it.
	retryBase = 10 * time.Minute
	retryMax  = 24 * time.Hour
)

var (
	errDisabled   = errors.New("secret scanning is off")
	errOwnerMoved = errors.New("the repository changed owner while it was scanned")
	errAborted    = errors.New("scan aborted")
	errStopped    = errors.New("stopped")
	errStoreFail  = errors.New(errStore)

	// repoScans holds the repositories being scanned in this process: the unique queue forgets an ID once it is
	// popped, so a push during a scan queues the repository again, and the second scan must wait for the first.
	repoScans sync_module.MutexMap

	// scanContent runs the detector and says whether it scanned all of the content (false: cut at the detector's size
	// limit, or a rule ran out of its budget). It computes the keyed fingerprints, so it panics before the keying is
	// set up. A test replaces it.
	scanContent = func(d *secretdetection.Detector, content []byte) ([]secretdetection.Finding, bool) {
		return d.Scan(content)
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
// alerts and the new state. One call is one slice: at most MAX_SCAN_BYTES and MAX_SCAN_BLOBS of objects within
// SCAN_TIMEOUT. A repository with more is continued by the next slice, which is queued behind the other repositories,
// so one big repository never holds up the rest. A scan that fails keeps what it stored and backs off; a scan that
// is overtaken (a push meanwhile, a transfer, another process) is queued again.
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
	unlock := repoScans.Lock(strconv.FormatInt(repo.ID, 10))
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

	s := &scanner{ctx: ctx, scanCtx: scanCtx, repo: repo, cfg: cfg, detector: detector}
	kinds := []string{secretscan_model.KindRepo}
	if repo.HasWiki() {
		kinds = append(kinds, secretscan_model.KindWiki)
	}
	for _, kind := range kinds {
		rq, kerr := s.scanKind(kind)
		requeue = requeue || rq
		err = errors.Join(err, kerr)
	}
	return requeue, err
}

// scanner is the state of one slice of a scan of a repository, for the repository and then the wiki.
type scanner struct {
	ctx, scanCtx context.Context
	repo         *repo_model.Repository
	cfg          setting.SecretScanningOptions
	detector     *secretdetection.Detector
	// the work of the units stored in this slice, over both git repositories: the limits are for the slice
	blobs, bytes int64
	progress     bool // a unit was stored in this slice
}

// blobFinding is a finding with its fingerprint: the secret itself is gone.
type blobFinding struct {
	ruleID, fingerprint string
	start, end          int
}

// foundBlob is a file with findings and the places git says it is at.
type foundBlob struct {
	name     string // the name rev-list gave, for a blob that no commit of the unit brings
	findings []blobFinding
	locs     map[string]*change // by path, the oldest commit of the unit
	extra    int                // places over the cap, counted
}

// unit is the work of one pass over the objects reachable from some tips and not from the tips done before. Its
// result is stored only when it is whole; a pass that a limit stops is dropped (or retried with fewer commits).
type unit struct {
	blobs, bytes, skipped int64
	partial               int64  // blobs scanned only in part
	stop                  string // the limit that ended the pass
	lines, stopLine       int    // listed objects, and how many were listed when the limit was hit
	locBytes              int
	keyCap                int64
	capped                bool // findings left out for the cap on distinct secrets
	found                 map[string]*foundBlob
	keys                  map[string]struct{} // distinct (rule, fingerprint)
	err                   error
}

// run is the scan of one git repository (the repository or its wiki) in one slice.
type run struct {
	*scanner
	kind    string
	gitRepo *git.Repository
	old     *secretscan_model.Scan // the row as stored last, nil when there is none
	done    []string               // tips whose reachable set is scanned
	// the totals of the scan under way (over its slices), as stored in the row
	totBlobs, totBytes, totSkipped int64
	partial                        int64
	capped                         bool
	carry                          bool // a failure is waiting out its backoff: the stored retry fields stay as they are
	maxBlobs, maxBytes             int64
	buf                            []byte
	u                              *unit
}

// candidate is an alert to store: a secret and the places it was found at in this scan.
type candidate struct {
	ruleID, fingerprint string
	locs                map[string]*secretscan_model.Location // by path hash and line
	extra               int
}

// abort is an error that ends the slice quietly: the scan was overtaken or switched off.
type abort struct{ requeue bool }

func (*abort) Error() string { return "scan aborted" }

func (s *scanner) scanKind(kind string) (requeue bool, err error) {
	if s.scanCtx.Err() != nil {
		return s.progress, nil // the slice's time is up: this one is next (not a failure of its git repository)
	}
	old, err := secretscan_model.GetScan(s.ctx, s.repo.ID, kind)
	if err != nil {
		return false, err
	}
	r := &run{scanner: s, kind: kind, old: old}
	open, defaultBranch := gitrepo.OpenRepository, s.repo.DefaultBranch
	if kind == secretscan_model.KindWiki {
		open, defaultBranch = gitrepo.OpenWikiRepository, s.repo.GetWikiBranchName()
	}
	if r.gitRepo, err = open(s.scanCtx, s.repo); err != nil {
		return r.end(nil, errGit, "", 0)
	}
	defer r.gitRepo.Close()

	tips, err := readTips(s.scanCtx, r.gitRepo.Path, defaultBranch)
	if err != nil {
		return r.endOr(nil, errGit, 0)
	}
	if len(tips) > maxTips {
		return r.end(nil, errTooManyRef, "", 0)
	}
	if old != nil && old.Status == secretscan_model.StatusOK && len(notIn(tips, r.storedTips())) == 0 {
		return false, nil // nothing new, e.g. a push that only moved a ref to a commit that is scanned
	}
	afterTips(s.repo.ID, kind)
	if r.done, err = existingTips(s.scanCtx, r.gitRepo, r.storedTips()); err != nil {
		return r.endOr(tips, errGit, 0)
	}
	return r.slice(tips)
}

// storedTips are the tips the state row says are scanned; a state that can't be read scans everything again.
func (r *run) storedTips() []string {
	var tips []string
	if r.old != nil && r.old.ScannedTips != "" {
		_ = json.Unmarshal([]byte(r.old.ScannedTips), &tips)
	}
	return tips
}

// notIn are the tips that are not in done, in their order.
func notIn(tips []tip, done []string) []tip {
	seen := make(map[string]struct{}, len(done))
	for _, id := range done {
		seen[id] = struct{}{}
	}
	var out []tip
	for _, t := range tips {
		if _, ok := seen[t.id]; !ok {
			out = append(out, t)
		}
	}
	return out
}

// slice works through the tips not done yet, group by group, a unit at a time, and stores the result of each
// unit that is whole. It ends when everything is done, a limit is hit, or the time is up.
func (r *run) slice(tips []tip) (requeue bool, err error) {
	now := timeutil.TimeStampNow()
	gate := int(^uint(0) >> 1)
	r.maxBlobs, r.maxBytes = r.cfg.MaxScanBlobs, r.cfg.MaxScanBytes
	if old := r.old; old != nil {
		if old.RetryUnix > 0 { // a scan under way or waiting out a failure: its totals go on
			r.totBlobs, r.totBytes, r.totSkipped = old.Blobs, old.Bytes, old.SkippedLarge
		}
		if old.FailCount > 0 && now < old.RetryUnix {
			// a failure waits out its backoff: the groups before the one that failed are scanned now (a push to
			// the default branch must not wait for the junk in a pull ref), the rest only when the time has come
			gate, r.carry = old.RetryGroup, true
		}
		if old.Error == stopTimeout && old.FailCount > 0 { // it timed out: a smaller slice next time
			shift := min(old.FailCount, 30)
			r.maxBlobs = max(r.maxBlobs>>shift, 1)
			r.maxBytes = max(r.maxBytes>>shift, min(r.maxBytes, 1<<20))
		}
	}
	var stop string
	group := 0
	for {
		pending := notIn(tips, r.done)
		if len(pending) == 0 || pending[0].group >= gate {
			break
		}
		if r.ctx.Err() != nil {
			return false, r.ctx.Err() // shutting down: what is stored stays, the next start goes on
		}
		group = pending[0].group
		if r.scanCtx.Err() != nil {
			stop = stopTimeout
			break
		}
		n := 1
		for n < len(pending) && n < unitTips && pending[n].group == group {
			n++
		}
		var uerr error
		if stop, uerr = r.runUnit(group, tipIDs(pending[:n])); uerr != nil {
			var ab *abort
			if errors.As(uerr, &ab) {
				return ab.requeue, nil
			}
			if r.ctx.Err() != nil {
				return false, r.ctx.Err()
			}
			if errors.Is(uerr, errStoreFail) {
				return false, uerr // store has recorded it
			}
			return r.end(tips, errorCode(uerr), "", group)
		}
		if stop != "" {
			break
		}
	}
	return r.end(tips, "", stop, group)
}

// errorCode is the code stored for a failed pass: a panic, or git.
func errorCode(err error) string {
	if errors.Is(err, errAborted) {
		return errPanic
	}
	return errGit
}

// runUnit reads one unit and stores it. A limit that stops it ends the slice (the unit is dropped) when the slice
// has stored something already; if not, the unit alone is over the limits, and it is cut to the commits that fit:
// a prefix of them is stored and the next slice goes on after it.
func (r *run) runUnit(group int, ids []string) (stop string, err error) {
	u := r.attempt(group, ids)
	if u.err != nil {
		return "", u.err
	}
	if u.stop == "" {
		return "", r.storeUnit(group, ids, u)
	}
	if r.progress || u.stop == stopTimeout {
		return u.stop, nil
	}
	commits, err := listCommits(r.scanCtx, r.gitRepo.Path, rangeInput(ids, r.done), maxCommits)
	if err != nil {
		if r.scanCtx.Err() != nil {
			return stopTimeout, nil
		}
		return "", err
	}
	if len(commits) < 2 {
		return u.stop, nil // one commit (or nothing but a ref to a file) is over the limits by itself
	}
	// the share of the objects read before the limit was hit says how many commits may fit; a bit fewer, to be safe
	k := int(float64(len(commits)) * float64(u.stopLine) / float64(max(u.lines, 1)) * 0.8)
	k = min(max(k, 1), len(commits)-1)
	for range shrinkAttempts {
		prefix := heads(commits[:k])
		p := r.attempt(group, prefix)
		if p.err != nil {
			return "", p.err
		}
		if p.stop == "" {
			return u.stop, r.storeUnit(group, prefix, p)
		}
		if r.scanCtx.Err() != nil {
			return stopTimeout, nil
		}
		if k == 1 {
			break
		}
		k /= 2
	}
	return u.stop, nil
}

// attempt reads the objects reachable from the tips and not from the tips done, and finds the places of the
// findings. It stores nothing.
func (r *run) attempt(group int, ids []string) *unit {
	keyCap := r.cfg.MaxAlertsPerRepo
	if group == groupPull {
		keyCap = max(keyCap/2, 1) // as many secrets as the alert share of the pull refs
	}
	u := &unit{found: map[string]*foundBlob{}, keys: map[string]struct{}{}, keyCap: keyCap}
	r.u = u
	if err := r.enumerate(ids); err != nil {
		u.err = err
		return u
	}
	if u.stop == "" && len(u.found) > 0 && r.scanCtx.Err() == nil {
		if err := r.attribute(ids); err != nil {
			u.err = err
			return u
		}
	}
	if u.stop == "" && r.scanCtx.Err() != nil {
		u.stop = stopTimeout // dropped whole: no place is made up for blobs the time ran out on
	}
	return u
}

// enumerate reads every blob of the range once and keeps the findings. A limit that is hit stops the reading (the
// listing is counted to the end, to tell how much of the unit was read); u.stop says which one.
func (r *run) enumerate(ids []string) error {
	return r.gitRepo.WithCatFileBatchCheck(r.scanCtx, func(cw io.Writer, cr *bufio.Reader) error {
		return r.gitRepo.WithCatFileBatch(r.scanCtx, func(bw io.Writer, br *bufio.Reader) error {
			err := listObjects(r.scanCtx, r.gitRepo.Path, rangeInput(ids, r.done), func(id, name []byte) error {
				r.u.lines++
				if r.u.stop != "" {
					return nil
				}
				if r.scanCtx.Err() != nil {
					return errStopped
				}
				return r.object(cw, cr, bw, br, string(id), string(name))
			})
			if r.scanCtx.Err() != nil {
				return nil // the time, not git, ended the pass; the caller drops it
			}
			return err
		})
	})
}

// limit records that a limit ended the pass.
func (u *unit) limit(code string) {
	u.stop, u.stopLine = code, u.lines
}

// object checks the type and size of one object, and reads and scans it when it is a blob within the limits.
func (r *run) object(cw io.Writer, cr *bufio.Reader, bw io.Writer, br *bufio.Reader, id, name string) error {
	u := r.u
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
		u.skipped++
		return nil // never read
	}
	if r.scanner.blobs+u.blobs+1 > r.maxBlobs {
		u.limit(stopBlobs)
		return nil
	}
	if r.scanner.bytes+u.bytes+size > r.maxBytes {
		u.limit(stopBytes)
		return nil
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
	u.blobs++
	u.bytes += size
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
	u := r.u
	findings, complete := scanContent(r.detector, content)
	if !complete {
		u.partial++
	}
	if len(findings) == 0 {
		return nil
	}
	fb := &foundBlob{name: name, locs: map[string]*change{}}
	for _, f := range findings {
		bf := blobFinding{ruleID: f.RuleID, fingerprint: f.Fingerprint, start: f.StartLine, end: f.EndLine}
		key := bf.ruleID + "|" + bf.fingerprint
		if _, seen := u.keys[key]; !seen {
			if int64(len(u.keys)) >= u.keyCap {
				u.capped = true // more distinct secrets than alerts are kept: this one is left out
				continue
			}
			u.keys[key] = struct{}{}
		}
		fb.findings = append(fb.findings, bf)
	}
	if len(fb.findings) == 0 {
		return nil
	}
	if len(u.found) >= maxFindingBlobs {
		u.limit(stopFindings)
		return nil
	}
	u.found[id] = fb
	return nil
}

// attribute asks git for the commit, path and author of every blob with findings, over the same range. A blob keeps
// at most MAX_LOCATIONS_PER_ALERT places (no alert stores more), the rest are counted; so are the places over the
// unit's text budget.
func (r *run) attribute(ids []string) error {
	u := r.u
	err := listChanges(r.scanCtx, r.gitRepo.Path, rangeInput(ids, r.done), func(c change) {
		fb := u.found[c.NewID]
		if fb == nil {
			return
		}
		if old, ok := fb.locs[c.Path]; ok {
			if c.CommitUnix < old.CommitUnix { // log is newest first: the oldest commit of the range brought it
				*old = c
			}
			return
		}
		if int64(len(fb.locs)) >= r.cfg.MaxLocationsPerAlert || u.locBytes > maxUnitLocBytes {
			fb.extra++
			return
		}
		u.locBytes += len(c.Path) + len(c.AuthorName) + len(c.AuthorEmail) + 128
		fb.locs[c.Path] = &c
	})
	if r.scanCtx.Err() != nil {
		return nil // the time ended it: attempt drops the unit
	}
	return err
}

// candidates turns the findings of the blobs into alerts with their places. An alert keeps at most
// MAX_LOCATIONS_PER_ALERT places while it is built, the others are counted.
func (r *run) candidates(u *unit) []*candidate {
	byKey := map[string]*candidate{}
	for _, id := range slices.Sorted(maps.Keys(u.found)) {
		fb := u.found[id]
		places := slices.SortedFunc(maps.Values(fb.locs), func(a, b *change) int { return cmp.Compare(a.Path, b.Path) })
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
				key := fmt.Sprintf("%s:%d", secretscan_model.PathHash(p.Path), f.start)
				have := c.locs[key]
				if have == nil && int64(len(c.locs)) >= r.cfg.MaxLocationsPerAlert {
					c.extra++
					continue
				}
				loc := secretscan_model.NewLocation(r.repo.ID, r.kind, id, p.Path, f.start, f.end, p.Commit, p.AuthorName, p.AuthorEmail, p.CommitUnix)
				if have == nil || older(loc, have) {
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

// state is the row for this scan: the tips done and the totals so far.
func (r *run) state() secretscan_model.Scan {
	tipsJSON, _ := json.Marshal(r.done) // a list of strings
	return secretscan_model.Scan{
		RepoID: r.repo.ID, Kind: r.kind, ScannedTips: string(tipsJSON),
		Blobs: r.totBlobs, Bytes: r.totBytes, SkippedLarge: r.totSkipped,
	}
}

// storeUnit stores the alerts of a whole unit and the new tips done, in one transaction. The state it leaves says
// "under way" with a retry time, until end writes the final one.
func (r *run) storeUnit(group int, add []string, u *unit) error {
	done := slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(r.done), add...))))
	saved := *r
	r.done = done
	r.totBlobs += u.blobs
	r.totBytes += u.bytes
	r.totSkipped += u.skipped
	st := r.state()
	if r.carry {
		st.Status, st.Error, st.FailCount, st.RetryUnix, st.RetryGroup = r.old.Status, r.old.Error, r.old.FailCount, r.old.RetryUnix, r.old.RetryGroup
	} else {
		st.Status, st.Error, st.RetryUnix = secretscan_model.StatusIncomplete, stopSlice, timeutil.TimeStampNow().Add(int64(retryBase/time.Second))
	}
	capped, err := r.store(&st, group, r.candidates(u))
	if err != nil {
		r.done, r.totBlobs, r.totBytes, r.totSkipped = saved.done, saved.totBlobs, saved.totBytes, saved.totSkipped
		return err
	}
	r.scanner.blobs += u.blobs
	r.scanner.bytes += u.bytes
	r.scanner.progress = true
	r.capped = r.capped || capped || u.capped
	r.partial += u.partial
	return nil
}

// end writes the state the slice leaves, after the units it stored. endErr is the code of a failure (git, a panic),
// stop the limit that ended the slice. A failure, or a limit hit with nothing stored by this slice, backs off; a
// limit hit after progress asks for the next slice at once.
func (r *run) end(tips []tip, endErr, stop string, group int) (requeue bool, err error) {
	if endErr == "" && (stop == "" || r.scanner.progress) {
		pending := notIn(tips, r.done)
		switch {
		case stop != "":
			return true, r.finish(secretscan_model.StatusIncomplete, stop, true)
		case len(pending) > 0:
			return false, nil // waiting out a failure: the groups before it are done (and stored)
		}
		// all done: the tips of the refs stand for everything, the commits that stood in for part of a tip are gone
		r.done = tipIDs(tips)
		switch {
		case r.capped:
			return false, r.finish(secretscan_model.StatusIncomplete, stopAlerts, false)
		case r.partial > 0:
			return false, r.finish(secretscan_model.StatusIncomplete, stopPartial, false) // not retried: the same limit would end it again
		}
		return false, r.finish(secretscan_model.StatusOK, "", false)
	}
	code, status := cmp.Or(endErr, stop), secretscan_model.StatusFailed
	if endErr == "" {
		status = secretscan_model.StatusIncomplete
	}
	log.Warn("secret scanning: scan of repository %d (%s) failed: %s", r.repo.ID, r.kind, code)
	return false, r.fail(status, code, group)
}

// endOr is end for a failure that is no failure when the process is shutting down.
func (r *run) endOr(tips []tip, code string, group int) (bool, error) {
	if r.ctx.Err() != nil {
		return false, r.ctx.Err()
	}
	return r.end(tips, code, "", group)
}

// finish stores the final state of a slice that made progress: ok, or incomplete with the code of what limited it.
// A slice that goes on keeps a retry time, which lets the cron task pick it up if the queue loses it.
func (r *run) finish(status, code string, goOn bool) error {
	st := r.state()
	st.Status, st.Error = status, code
	if goOn {
		st.RetryUnix = timeutil.TimeStampNow().Add(int64(retryBase / time.Second))
	}
	return r.storeFinal(&st)
}

// fail stores a failed or limited slice that made no progress, with the backoff of the failures in a row.
func (r *run) fail(status, code string, group int) error {
	st := r.state()
	if r.old != nil && r.old.RetryUnix == 0 { // the counts of a scan that isn't under way are not this one's
		st.Blobs, st.Bytes, st.SkippedLarge = r.old.Blobs, r.old.Bytes, r.old.SkippedLarge
	}
	st.Status, st.Error, st.RetryGroup = status, code, group
	st.FailCount = 1
	if r.old != nil {
		st.FailCount = r.old.FailCount + 1
	}
	delay := min(retryBase<<min(st.FailCount-1, 12), retryMax)
	st.RetryUnix = timeutil.TimeStampNow().Add(int64(delay / time.Second))
	return r.storeFinal(&st)
}

// storeFinal is store without alerts; an overtaken scan is not an error of the repository.
func (r *run) storeFinal(st *secretscan_model.Scan) error {
	if _, err := r.store(st, 0, nil); err != nil {
		var ab *abort
		if errors.As(err, &ab) {
			return nil
		}
		return err
	}
	return nil
}

// store writes the alerts and the state row in one transaction, after the last check of the switches, and keeps
// the row it wrote for the next write. The write is discarded when secret scanning was turned off or the repository
// is gone, and the slice is dropped (abort, queued again) when the repository changed owner (the new owner's switch
// and rules decide) or another process wrote the state first.
func (r *run) store(st *secretscan_model.Scan, group int, cands []*candidate) (capped bool, err error) {
	cfg := setting.SecretScanningSettings(r.ctx) // before the transaction: it may query the database
	err = db.WithTx(r.ctx, func(ctx context.Context) error {
		enabled, owner, err := enabledStored(ctx, r.repo.ID)
		if err != nil {
			return err
		}
		if !enabled {
			return errDisabled
		}
		if owner != r.repo.OwnerID {
			return errOwnerMoved
		}
		if capped, err = writeAlerts(ctx, cfg, r.repo.ID, r.kind, group == groupPull, cands); err != nil {
			return err
		}
		return secretscan_model.Save(ctx, r.old, st)
	})
	switch {
	case err == nil:
		r.old, err = secretscan_model.GetScan(r.ctx, r.repo.ID, r.kind)
		return capped, err
	case errors.Is(err, errDisabled):
		return false, &abort{}
	case errors.Is(err, errOwnerMoved), errors.Is(err, secretscan_model.ErrScanMoved):
		return false, &abort{requeue: true}
	}
	// a write that lost a race on a unique key looks like any error: if the state row moved, the scan is overtaken
	if cur, rerr := secretscan_model.GetScan(r.ctx, r.repo.ID, r.kind); rerr == nil && ((cur == nil) != (r.old == nil) || cur != nil && r.old != nil && cur.Version != r.old.Version) {
		return false, &abort{requeue: true}
	}
	log.Error("secret scanning: store the scan of repository %d (%s): %v", r.repo.ID, r.kind, err)
	if st.Error != errStore { // say so in the state row too, with the backoff, so the cron task tries again later
		_ = r.fail(secretscan_model.StatusFailed, errStore, group)
	}
	return false, errStoreFail
}

// writeAlerts stores the alerts and their places, relying on the unique keys: an alert or a place that is already
// there is not stored again. It reads and writes in batches (a few queries per batch of alerts, none per place).
// Places over MAX_LOCATIONS_PER_ALERT are only counted. A new alert is left out (capped) when the repository has
// MAX_ALERTS_PER_REPO open alerts, or, for a secret only refs/pull/* reach (which any reader can write), when half of
// that many open alerts are of that kind: the branches and tags always have room.
func writeAlerts(ctx context.Context, cfg setting.SecretScanningOptions, repoID int64, kind string, viaPull bool, cands []*candidate) (capped bool, err error) {
	if len(cands) == 0 {
		return false, nil
	}
	total, pulls, err := secretscan_model.CountOpenAlerts(ctx, repoID)
	if err != nil {
		return false, err
	}
	pullCap := max(cfg.MaxAlertsPerRepo/2, 1)
	for batch := range slices.Chunk(cands, alertBatch) {
		fingerprints := make([]string, len(batch))
		for i, c := range batch {
			fingerprints[i] = c.fingerprint
		}
		existing, err := secretscan_model.GetAlertsByFingerprints(ctx, repoID, fingerprints)
		if err != nil {
			return false, err
		}
		byKey := map[string]*secretscan_model.Alert{}
		for _, a := range existing {
			byKey[a.RuleID+"|"+a.Fingerprint] = a
		}
		var alerts []*secretscan_model.Alert // by candidate; nil for one that was left out
		var ids, reached []int64
		for _, c := range batch {
			a := byKey[c.ruleID+"|"+c.fingerprint]
			switch {
			case a == nil:
				if total >= cfg.MaxAlertsPerRepo || viaPull && pulls >= pullCap {
					capped = true
					break
				}
				rule, _ := secretdetection.RuleByID(c.ruleID)
				a = &secretscan_model.Alert{RepoID: repoID, RuleID: c.ruleID, SecretType: rule.Type, Fingerprint: c.fingerprint, ViaPull: viaPull}
				if err := secretscan_model.InsertAlert(ctx, a); err != nil {
					return false, err
				}
				total++
				if viaPull {
					pulls++
				}
			case a.ViaPull && !viaPull:
				reached = append(reached, a.ID) // a branch or tag reaches it too
				if a.State == secretscan_model.StateOpen {
					pulls--
				}
			}
			alerts = append(alerts, a)
			if a != nil {
				ids = append(ids, a.ID)
			}
		}
		if err := secretscan_model.ClearViaPull(ctx, reached); err != nil {
			return false, err
		}
		places, err := secretscan_model.StoredPlaces(ctx, ids)
		if err != nil {
			return false, err
		}
		var rows []*secretscan_model.Location
		for i, c := range batch {
			a := alerts[i]
			if a == nil {
				continue
			}
			stored, seen := int64(len(places[a.ID])), c.extra
			for _, key := range slices.Sorted(maps.Keys(c.locs)) {
				loc := c.locs[key]
				if places[a.ID][secretscan_model.PlaceKey{Kind: kind, PathHash: loc.PathHash, StartLine: loc.StartLine}] {
					continue
				}
				seen++
				if stored >= cfg.MaxLocationsPerAlert {
					continue
				}
				loc.AlertID = a.ID
				rows = append(rows, loc)
				stored++
			}
			if seen > 0 {
				if err := secretscan_model.AddLocationCount(ctx, a.ID, seen); err != nil {
					return false, err
				}
			}
		}
		if err := secretscan_model.InsertLocations(ctx, rows); err != nil {
			return false, err
		}
	}
	return capped, nil
}
