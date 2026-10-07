// Copyright 2016 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package code

import (
	"context"
	"os"
	"runtime/pprof"
	"slices"
	"sync/atomic"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/modules/container"
	"forgejo.org/modules/graceful"
	"forgejo.org/modules/indexer/code/bleve"
	"forgejo.org/modules/indexer/code/elasticsearch"
	"forgejo.org/modules/indexer/code/internal"
	"forgejo.org/modules/indexer/code/zoekt"
	"forgejo.org/modules/log"
	"forgejo.org/modules/process"
	"forgejo.org/modules/queue"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
)

var (
	indexerQueue *queue.WorkerPoolQueue[*internal.IndexerData]
	// globalIndexer is the global indexer, it cannot be nil.
	// When the real indexer is not ready, it will be a dummy indexer which will return error to explain it's not ready.
	// So it's always safe use it as *globalIndexer.Load() and call its methods.
	globalIndexer atomic.Pointer[internal.Indexer]
	dummyIndexer  *internal.Indexer
)

func init() {
	i := internal.NewDummyIndexer()
	dummyIndexer = &i
	globalIndexer.Store(dummyIndexer)
}

func index(ctx context.Context, indexer internal.Indexer, repoID int64) error {
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if repo_model.IsErrRepoNotExist(err) {
		return indexer.Delete(ctx, repoID)
	}
	if err != nil {
		return err
	}

	repoTypes := setting.Indexer.RepoIndexerRepoTypes

	if len(repoTypes) == 0 {
		repoTypes = []string{"sources"}
	}

	// skip forks from being indexed if unit is not present
	if !slices.Contains(repoTypes, "forks") && repo.IsFork {
		return nil
	}

	// skip mirrors from being indexed if unit is not present
	if !slices.Contains(repoTypes, "mirrors") && repo.IsMirror {
		return nil
	}

	// skip templates from being indexed if unit is not present
	if !slices.Contains(repoTypes, "templates") && repo.IsTemplate {
		return nil
	}

	// skip regular repos from being indexed if unit is not present
	if !slices.Contains(repoTypes, "sources") && !repo.IsFork && !repo.IsMirror && !repo.IsTemplate {
		return nil
	}

	sha, err := getDefaultBranchSha(ctx, repo)
	if err != nil {
		return err
	}
	status, err := repo_model.GetIndexerStatus(ctx, repo, repo_model.RepoIndexerTypeCode)
	if err != nil {
		return err
	}
	wanted, err := expandIndexedBranches(ctx, repo)
	if err != nil {
		return err
	}
	stored, err := repo_model.GetIndexerBranches(ctx, repo.ID)
	if err != nil {
		return err
	}
	// an indexer that indexes whole repos only does so once there are other branches to index or to drop
	if bi, ok := indexer.(internal.BranchIndexer); ok && (len(wanted) > 0 || len(stored) > 0) {
		return indexAllBranches(ctx, bi, repo, sha, status, stored, wanted)
	}

	changes, err := getRepoChanges(ctx, repo, repo.DefaultBranch, status.CommitSha, sha)
	if err != nil {
		return err
	} else if changes == nil {
		return nil
	}

	if err := indexer.Index(ctx, repo, repo.DefaultBranch, sha, true, changes); err != nil {
		return err
	}

	if err := repo_model.UpdateIndexerStatus(ctx, repo, repo_model.RepoIndexerTypeCode, sha); err != nil {
		return err
	}
	return indexExtraBranches(ctx, indexer, repo, stored, wanted)
}

// indexAllBranches hands the default branch and the wanted ones to an indexer that indexes a
// repo as a whole, when any of them has a new commit or a branch has to go.
func indexAllBranches(ctx context.Context, bi internal.BranchIndexer, repo *repo_model.Repository, sha string,
	status *repo_model.RepoIndexerStatus, stored map[string]string, wanted []BranchInfo,
) error {
	changed := status.CommitSha != sha || len(stored) != len(wanted)
	for _, b := range wanted {
		changed = changed || stored[b.Name] != b.CommitID
	}
	if !changed {
		return nil
	}

	all := append([]BranchInfo{{Name: repo.DefaultBranch, CommitID: sha}}, wanted...)
	branches := make([]internal.BranchFiles, 0, len(all))
	for _, b := range all {
		changes, err := genesisChanges(ctx, repo, b.CommitID)
		if err != nil {
			return err
		}
		bf := internal.BranchFiles{Name: b.Name, CommitID: b.CommitID}
		if changes != nil {
			bf.Files = changes.Updates
		}
		branches = append(branches, bf)
	}
	if err := bi.IndexBranches(ctx, repo, repo.DefaultBranch, branches); err != nil {
		return err
	}

	if err := repo_model.UpdateIndexerStatus(ctx, repo, repo_model.RepoIndexerTypeCode, sha); err != nil {
		return err
	}
	keep := make(container.Set[string], len(wanted))
	for _, b := range wanted {
		keep.Add(b.Name)
		if err := repo_model.SetIndexerBranch(ctx, repo.ID, b.Name, b.CommitID); err != nil {
			return err
		}
	}
	for name := range stored {
		if !keep.Contains(name) {
			if err := repo_model.DeleteIndexerBranches(ctx, repo.ID, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// indexExtraBranches brings the indexed non-default branches up to date, and drops the ones
// that were deleted, no longer match a pattern or are over the limit.
func indexExtraBranches(ctx context.Context, indexer internal.Indexer, repo *repo_model.Repository, stored map[string]string, wanted []BranchInfo) error {
	keep := make(container.Set[string], len(wanted))
	for _, b := range wanted {
		keep.Add(b.Name)
	}
	for name := range stored {
		if keep.Contains(name) {
			continue
		}
		if name != repo.DefaultBranch { // the default branch's documents are not the extra ones
			if err := indexer.DeleteBranch(ctx, repo.ID, name); err != nil {
				return err
			}
		}
		if err := repo_model.DeleteIndexerBranches(ctx, repo.ID, name); err != nil {
			return err
		}
	}

	for _, b := range wanted {
		if stored[b.Name] == b.CommitID {
			continue
		}
		changes, err := getRepoChanges(ctx, repo, b.Name, stored[b.Name], b.CommitID)
		if err != nil {
			return err
		}
		if changes != nil {
			if err := indexer.Index(ctx, repo, b.Name, b.CommitID, false, changes); err != nil {
				return err
			}
		}
		if err := repo_model.SetIndexerBranch(ctx, repo.ID, b.Name, b.CommitID); err != nil {
			return err
		}
	}
	return nil
}

// ResetRepoIndexer drops everything indexed of a repo and indexes it again, for changes
// that make the stored documents wrong as a whole, like another default branch.
func ResetRepoIndexer(ctx context.Context, repo *repo_model.Repository) error {
	if !setting.Indexer.RepoIndexerEnabled {
		return nil
	}
	if err := (*globalIndexer.Load()).Delete(ctx, repo.ID); err != nil {
		return err
	}
	if err := repo_model.DeleteIndexerBranches(ctx, repo.ID); err != nil {
		return err
	}
	if _, err := db.GetEngine(ctx).Where("repo_id = ? AND indexer_type = ?", repo.ID, repo_model.RepoIndexerTypeCode).Delete(new(repo_model.RepoIndexerStatus)); err != nil {
		return err
	}
	repo.CodeIndexerStatus = nil
	UpdateRepoIndexer(repo)
	return nil
}

func setSearchOption(set bool, val string) {
	if set {
		if !slices.Contains(CodeSearchOptions, val) {
			CodeSearchOptions = append(CodeSearchOptions, val)
		}
	} else if i := slices.Index(CodeSearchOptions, val); i >= 0 {
		CodeSearchOptions = append(CodeSearchOptions[:i], CodeSearchOptions[i+1:]...)
	}
}

// Init initialize the repo indexer
func Init() {
	if !setting.Indexer.RepoIndexerEnabled {
		(*globalIndexer.Load()).Close()
		return
	}
	setSearchOption(setting.Indexer.RepoIndexerEnableFuzzy, "fuzzy")
	setSearchOption(setting.Indexer.RepoType == "zoekt", "regexp")

	ctx, cancel, finished := process.GetManager().AddTypedContext(context.Background(), "Service: CodeIndexer", process.SystemProcessType, false)

	graceful.GetManager().RunAtTerminate(func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		cancel()
		log.Debug("Closing repository indexer")
		(*globalIndexer.Load()).Close()
		log.Info("PID: %d Repository Indexer closed", os.Getpid())
		finished()
	})

	waitChannel := make(chan time.Duration, 1)

	// Create the Queue
	switch setting.Indexer.RepoType {
	case "bleve", "elasticsearch", "zoekt":
		handler := func(items ...*internal.IndexerData) (unhandled []*internal.IndexerData) {
			indexer := *globalIndexer.Load()
			// make it a process to allow for cancellation (especially during integration tests where no global shutdown happens)
			batchCtx, _, finished := process.GetManager().AddContext(ctx, "CodeIndexer batch")
			defer finished()
			for _, indexerData := range items {
				log.Trace("IndexerData Process Repo: %d", indexerData.RepoID)
				if err := index(batchCtx, indexer, indexerData.RepoID); err != nil {
					unhandled = append(unhandled, indexerData)
					if !setting.IsInTesting {
						log.Error("Codes indexer handler: index error for repo %v: %v", indexerData.RepoID, err)
					}
				}
			}
			return unhandled
		}

		indexerQueue = queue.CreateUniqueQueue(ctx, "code_indexer", handler)
		if indexerQueue == nil {
			log.Fatal("Unable to create codes indexer queue")
		}
	default:
		log.Fatal("Unknown codes indexer type; %s", setting.Indexer.RepoType)
	}

	go func() {
		pprof.SetGoroutineLabels(ctx)
		start := time.Now()
		var (
			rIndexer internal.Indexer
			existed  bool
			err      error
		)
		switch setting.Indexer.RepoType {
		case "bleve":
			log.Info("PID: %d Initializing Repository Indexer at: %s", os.Getpid(), setting.Indexer.RepoPath)
			defer func() {
				if err := recover(); err != nil {
					log.Error("PANIC whilst initializing repository indexer: %v\nStacktrace: %s", err, log.Stack(2))
					log.Error("The indexer files are likely corrupted and may need to be deleted")
					log.Error("You can completely remove the \"%s\" directory to make Forgejo recreate the indexes", setting.Indexer.RepoPath)
				}
			}()

			rIndexer = bleve.NewIndexer(setting.Indexer.RepoPath)
			existed, err = rIndexer.Init(ctx)
			if err != nil {
				cancel()
				(*globalIndexer.Load()).Close()
				close(waitChannel)
				log.Fatal("PID: %d Unable to initialize the bleve Repository Indexer at path: %s Error: %v", os.Getpid(), setting.Indexer.RepoPath, err)
			}
		case "elasticsearch":
			log.Info("PID: %d Initializing Repository Indexer at: %s", os.Getpid(), util.SanitizeCredentialURLs(setting.Indexer.RepoConnStr))
			defer func() {
				if err := recover(); err != nil {
					log.Error("PANIC whilst initializing repository indexer: %v\nStacktrace: %s", err, log.Stack(2))
					log.Error("The indexer files are likely corrupted and may need to be deleted")
					log.Error("You can completely remove the \"%s\" index to make Forgejo recreate the indexes", util.SanitizeCredentialURLs(setting.Indexer.RepoConnStr))
				}
			}()

			rIndexer = elasticsearch.NewIndexer(setting.Indexer.RepoConnStr, setting.Indexer.RepoIndexerName)
			existed, err = rIndexer.Init(ctx)
			if err != nil {
				cancel()
				(*globalIndexer.Load()).Close()
				close(waitChannel)
				log.Fatal("PID: %d Unable to initialize the elasticsearch Repository Indexer connstr: %s Error: %v", os.Getpid(), util.SanitizeCredentialURLs(setting.Indexer.RepoConnStr), err)
			}
		case "zoekt":
			log.Info("PID: %d Initializing Repository Indexer at: %s", os.Getpid(), setting.Indexer.RepoPath)
			defer func() {
				if err := recover(); err != nil {
					log.Error("PANIC whilst initializing repository indexer: %v\nStacktrace: %s", err, log.Stack(2))
					log.Error("The indexer files are likely corrupted and may need to be deleted")
					log.Error("You can completely remove the \"%s\" directory to make Gitea recreate the indexes", setting.Indexer.RepoPath)
				}
			}()

			rIndexer = zoekt.NewIndexer(setting.Indexer.RepoPath)
			existed, err = rIndexer.Init(ctx)
			if err != nil {
				cancel()
				(*globalIndexer.Load()).Close()
				close(waitChannel)

				log.Fatal("PID: %d Unable to initialize the zoekt Repository Indexer at path: %s Error: %v", os.Getpid(), setting.Indexer.RepoPath, err)
			}

		default:
			log.Fatal("PID: %d Unknown Indexer type: %s", os.Getpid(), setting.Indexer.RepoType)
		}

		globalIndexer.Store(&rIndexer)

		// Start processing the queue
		go graceful.GetManager().RunWithCancel(indexerQueue)

		if !existed { // populate the index because it's created for the first time
			go graceful.GetManager().RunWithShutdownContext(populateRepoIndexer)
		}
		select {
		case waitChannel <- time.Since(start):
			break
		case <-graceful.GetManager().IsShutdown():
			break
		}

		close(waitChannel)
	}()

	if setting.Indexer.StartupTimeout > 0 {
		go func() {
			pprof.SetGoroutineLabels(ctx)
			timeout := setting.Indexer.StartupTimeout
			if graceful.GetManager().IsChild() && setting.GracefulHammerTime > 0 {
				timeout += setting.GracefulHammerTime
			}
			select {
			case <-graceful.GetManager().IsShutdown():
				log.Warn("Shutdown before Repository Indexer completed initialization")
				cancel()
				(*globalIndexer.Load()).Close()
			case duration, ok := <-waitChannel:
				if !ok {
					log.Warn("Repository Indexer Initialization failed")
					cancel()
					(*globalIndexer.Load()).Close()
					return
				}
				log.Info("Repository Indexer Initialization took %v", duration)
			case <-time.After(timeout):
				cancel()
				(*globalIndexer.Load()).Close()
				log.Fatal("Repository Indexer Initialization Timed-Out after: %v", timeout)
			}
		}()
	}
}

// UpdateRepoIndexer update a repository's entries in the indexer
func UpdateRepoIndexer(repo *repo_model.Repository) {
	indexData := &internal.IndexerData{RepoID: repo.ID}
	if err := indexerQueue.Push(indexData); err != nil {
		log.Error("Update repo index data %v failed: %v", indexData, err)
	}
}

// IsAvailable checks if issue indexer is available
func IsAvailable(ctx context.Context) bool {
	return (*globalIndexer.Load()).Ping(ctx) == nil
}

// populateRepoIndexer populate the repo indexer with pre-existing data. This
// should only be run when the indexer is created for the first time.
func populateRepoIndexer(ctx context.Context) {
	log.Info("Populating the repo indexer with existing repositories")

	exist, err := db.IsTableNotEmpty("repository")
	if err != nil {
		log.Fatal("System error: %v", err)
	} else if !exist {
		return
	}

	// if there is any existing repo indexer metadata in the DB, delete it
	// since we are starting afresh. Also, xorm requires deletes to have a
	// condition, and we want to delete everything, thus 1=1.
	for _, table := range []string{"repo_indexer_status", "repo_indexer_branch"} {
		if err := db.DeleteAllRecords(table); err != nil {
			log.Fatal("System error: %v", err)
		}
	}

	var maxRepoID int64
	if maxRepoID, err = db.GetMaxID("repository"); err != nil {
		log.Fatal("System error: %v", err)
	}

	// start with the maximum existing repo ID and work backwards, so that we
	// don't include repos that are created after gitea starts; such repos will
	// already be added to the indexer, and we don't need to add them again.
	for maxRepoID > 0 {
		select {
		case <-ctx.Done():
			log.Info("Repository Indexer population shutdown before completion")
			return
		default:
		}
		ids, err := repo_model.GetUnindexedRepos(ctx, repo_model.RepoIndexerTypeCode, maxRepoID, 0, 50)
		if err != nil {
			log.Error("populateRepoIndexer: %v", err)
			return
		} else if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			select {
			case <-ctx.Done():
				log.Info("Repository Indexer population shutdown before completion")
				return
			default:
			}
			if err := indexerQueue.Push(&internal.IndexerData{RepoID: id}); err != nil {
				log.Error("indexerQueue.Push: %v", err)
				return
			}
			maxRepoID = id - 1
		}
	}
	log.Info("Done (re)populating the repo indexer with existing repositories")
}
