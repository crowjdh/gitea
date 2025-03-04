// Copyright 2016 The Gitea Authors. All rights reserved.
// Use of this source code is governed by a MIT-style
// license that can be found in the LICENSE file.

package code

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"code.gitea.io/gitea/models/db"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/modules/graceful"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/queue"
	"code.gitea.io/gitea/modules/setting"
	"code.gitea.io/gitea/modules/timeutil"
)

// SearchResult result of performing a search in a repo
type SearchResult struct {
	RepoID      int64
	StartIndex  int
	EndIndex    int
	Filename    string
	Content     string
	CommitID    string
	UpdatedUnix timeutil.TimeStamp
	Language    string
	Color       string
}

// SearchResultLanguages result of top languages count in search results
type SearchResultLanguages struct {
	Language string
	Color    string
	Count    int
}

// Indexer defines an interface to index and search code contents
type Indexer interface {
	Index(repo *repo_model.Repository, sha string, changes *repoChanges) error
	Delete(repoID int64) error
	Search(repoIDs []int64, language, keyword string, page, pageSize int, isMatch bool) (int64, []*SearchResult, []*SearchResultLanguages, error)
	Close()
}

func filenameIndexerID(repoID int64, filename string) string {
	return indexerID(repoID) + "_" + filename
}

func indexerID(id int64) string {
	return strconv.FormatInt(id, 36)
}

func parseIndexerID(indexerID string) (int64, string) {
	index := strings.IndexByte(indexerID, '_')
	if index == -1 {
		log.Error("Unexpected ID in repo indexer: %s", indexerID)
	}
	repoID, _ := strconv.ParseInt(indexerID[:index], 36, 64)
	return repoID, indexerID[index+1:]
}

func filenameOfIndexerID(indexerID string) string {
	index := strings.IndexByte(indexerID, '_')
	if index == -1 {
		log.Error("Unexpected ID in repo indexer: %s", indexerID)
	}
	return indexerID[index+1:]
}

// IndexerData represents data stored in the code indexer
type IndexerData struct {
	RepoID int64
}

var (
	indexerQueue queue.UniqueQueue
)

func index(indexer Indexer, repoID int64) error {
	repo, err := repo_model.GetRepositoryByID(repoID)
	if repo_model.IsErrRepoNotExist(err) {
		return indexer.Delete(repoID)
	}
	if err != nil {
		return err
	}

	sha, err := getDefaultBranchSha(repo)
	if err != nil {
		return err
	}
	changes, err := getRepoChanges(repo, sha)
	if err != nil {
		return err
	} else if changes == nil {
		return nil
	}

	if err := indexer.Index(repo, sha, changes); err != nil {
		return err
	}

	return repo_model.UpdateIndexerStatus(repo, repo_model.RepoIndexerTypeCode, sha)
}

// Init initialize the repo indexer
func Init() {
	if !setting.Indexer.RepoIndexerEnabled {
		indexer.Close()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	graceful.GetManager().RunAtTerminate(func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		cancel()
		log.Debug("Closing repository indexer")
		indexer.Close()
		log.Info("PID: %d Repository Indexer closed", os.Getpid())
	})

	waitChannel := make(chan time.Duration, 1)

	// Create the Queue
	switch setting.Indexer.RepoType {
	case "bleve", "elasticsearch":
		handler := func(data ...queue.Data) {
			idx, err := indexer.get()
			if idx == nil || err != nil {
				log.Error("Codes indexer handler: unable to get indexer!")
				return
			}

			for _, datum := range data {
				indexerData, ok := datum.(*IndexerData)
				if !ok {
					log.Error("Unable to process provided datum: %v - not possible to cast to IndexerData", datum)
					continue
				}
				log.Trace("IndexerData Process Repo: %d", indexerData.RepoID)

				if err := index(indexer, indexerData.RepoID); err != nil {
					log.Error("index: %v", err)
					continue
				}
			}
		}

		indexerQueue = queue.CreateUniqueQueue("code_indexer", handler, &IndexerData{})
		if indexerQueue == nil {
			log.Fatal("Unable to create codes indexer queue")
		}
	default:
		log.Fatal("Unknown codes indexer type; %s", setting.Indexer.RepoType)
	}

	go func() {
		start := time.Now()
		var (
			rIndexer Indexer
			populate bool
			err      error
		)
		switch setting.Indexer.RepoType {
		case "bleve":
			log.Info("PID: %d Initializing Repository Indexer at: %s", os.Getpid(), setting.Indexer.RepoPath)
			defer func() {
				if err := recover(); err != nil {
					log.Error("PANIC whilst initializing repository indexer: %v\nStacktrace: %s", err, log.Stack(2))
					log.Error("The indexer files are likely corrupted and may need to be deleted")
					log.Error("You can completely remove the \"%s\" directory to make Gitea recreate the indexes", setting.Indexer.RepoPath)
				}
			}()

			rIndexer, populate, err = NewBleveIndexer(setting.Indexer.RepoPath)
			if err != nil {
				cancel()
				indexer.Close()
				close(waitChannel)
				log.Fatal("PID: %d Unable to initialize the bleve Repository Indexer at path: %s Error: %v", os.Getpid(), setting.Indexer.RepoPath, err)
			}
		case "elasticsearch":
			log.Info("PID: %d Initializing Repository Indexer at: %s", os.Getpid(), setting.Indexer.RepoConnStr)
			defer func() {
				if err := recover(); err != nil {
					log.Error("PANIC whilst initializing repository indexer: %v\nStacktrace: %s", err, log.Stack(2))
					log.Error("The indexer files are likely corrupted and may need to be deleted")
					log.Error("You can completely remove the \"%s\" index to make Gitea recreate the indexes", setting.Indexer.RepoConnStr)
				}
			}()

			rIndexer, populate, err = NewElasticSearchIndexer(setting.Indexer.RepoConnStr, setting.Indexer.RepoIndexerName)
			if err != nil {
				cancel()
				indexer.Close()
				close(waitChannel)
				log.Fatal("PID: %d Unable to initialize the elasticsearch Repository Indexer connstr: %s Error: %v", os.Getpid(), setting.Indexer.RepoConnStr, err)
			}
		default:
			log.Fatal("PID: %d Unknown Indexer type: %s", os.Getpid(), setting.Indexer.RepoType)
		}

		indexer.set(rIndexer)

		// Start processing the queue
		go graceful.GetManager().RunWithShutdownFns(indexerQueue.Run)

		if populate {
			go graceful.GetManager().RunWithShutdownContext(populateRepoIndexer)
		}
		select {
		case waitChannel <- time.Since(start):
		case <-graceful.GetManager().IsShutdown():
		}

		close(waitChannel)
	}()

	if setting.Indexer.StartupTimeout > 0 {
		go func() {
			timeout := setting.Indexer.StartupTimeout
			if graceful.GetManager().IsChild() && setting.GracefulHammerTime > 0 {
				timeout += setting.GracefulHammerTime
			}
			select {
			case <-graceful.GetManager().IsShutdown():
				log.Warn("Shutdown before Repository Indexer completed initialization")
				cancel()
				indexer.Close()
			case duration, ok := <-waitChannel:
				if !ok {
					log.Warn("Repository Indexer Initialization failed")
					cancel()
					indexer.Close()
					return
				}
				log.Info("Repository Indexer Initialization took %v", duration)
			case <-time.After(timeout):
				cancel()
				indexer.Close()
				log.Fatal("Repository Indexer Initialization Timed-Out after: %v", timeout)
			}
		}()
	}
}

// UpdateRepoIndexer update a repository's entries in the indexer
func UpdateRepoIndexer(repo *repo_model.Repository) {
	indexData := &IndexerData{RepoID: repo.ID}
	if err := indexerQueue.Push(indexData); err != nil {
		log.Error("Update repo index data %v failed: %v", indexData, err)
	}
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
	if err := db.DeleteAllRecords("repo_indexer_status"); err != nil {
		log.Fatal("System error: %v", err)
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
		ids, err := repo_model.GetUnindexedRepos(repo_model.RepoIndexerTypeCode, maxRepoID, 0, 50)
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
			if err := indexerQueue.Push(&IndexerData{RepoID: id}); err != nil {
				log.Error("indexerQueue.Push: %v", err)
				return
			}
			maxRepoID = id - 1
		}
	}
	log.Info("Done (re)populating the repo indexer with existing repositories")
}
