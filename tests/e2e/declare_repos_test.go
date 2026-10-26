// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	vulnerability_model "forgejo.org/models/vulnerability"
	"forgejo.org/modules/git"
	"forgejo.org/modules/gitrepo"
	"forgejo.org/modules/indexer/stats"
	"forgejo.org/modules/setting"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/vulnerability"
	actions_service "forgejo.org/services/actions"
	issue_service "forgejo.org/services/issue"
	pull_service "forgejo.org/services/pull"
	release_service "forgejo.org/services/release"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/services/wiki"
	"forgejo.org/tests/forgery"

	"code.forgejo.org/forgejo/runner/v13/act/jobparser"
	"code.forgejo.org/xorm/xorm/convert"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// first entry represents filename
// the following entries define the full file content over time
type FileChanges struct {
	Filename  string
	CommitMsg string
	Versions  []string
}

// performs additional repo setup as needed
type SetupRepo func(*user_model.User, *repo_model.Repository)

// put your Git repo declarations in here
// feel free to amend the helper function below or use the raw variant directly
func DeclareGitRepos(t *testing.T) {
	now := timeutil.TimeStampNow()
	postIssue := func(repo *repo_model.Repository, user *user_model.User, age int64, title, content string) {
		issue := &issues_model.Issue{
			RepoID:      repo.ID,
			PosterID:    user.ID,
			Title:       title,
			Content:     content,
			CreatedUnix: now.Add(-age),
		}
		require.NoError(t, issue_service.NewIssue(db.DefaultContext, repo, issue, nil, nil, nil))
	}
	postPullRequest := func(repo *repo_model.Repository, branchName string, user *user_model.User, age int64, title, content string) {
		issue := &issues_model.Issue{
			RepoID:      repo.ID,
			PosterID:    user.ID,
			Poster:      user,
			Title:       title,
			Content:     content,
			CreatedUnix: now.Add(-age),
			IsPull:      true,
		}
		pr := &issues_model.PullRequest{
			Issue:      issue,
			HeadRepoID: issue.RepoID,
			BaseRepoID: issue.RepoID,
			HeadBranch: branchName,
			BaseBranch: repo.DefaultBranch,
			Status:     issues_model.PullRequestStatusMergeable,
		}
		require.NoError(t, pull_service.NewPullRequest(db.DefaultContext, repo, issue, []int64{}, []string{}, pr, []int64{}))
	}

	newRepo(t, 2, "diff-test", nil, []FileChanges{{
		Filename: "testfile",
		Versions: []string{"hello", "hallo", "hola", "native", "ubuntu-latest", "- runs-on: ubuntu-latest", "- runs-on: debian-latest"},
	}}, nil)
	newRepo(t, 2, "bitbucket-migrated", nil, []FileChanges{{
		Filename: "main.go",
		Versions: []string{"hello"},
	}}, func(_ *user_model.User, repo *repo_model.Repository) {
		require.NoError(t, db.Insert(db.DefaultContext, &repo_model.BitbucketOrigin{RepoID: repo.ID, ProjectKey: "PAY", Slug: "api", Server: "https://bb.example.com"}))
		_, err := db.Exec(db.DefaultContext, "UPDATE repository SET original_service_type = ? WHERE id = ?", api.BitbucketDataCenterService, repo.ID)
		require.NoError(t, err)
	})
	// the Dependencies page (dependencies.test.e2e.ts): a stored scan of the head, as a push to the default branch would
	// leave. They belong to user11, not to user2: more user2 repositories push others off the first page of the
	// repository pickers (user-settings.test.e2e.ts).
	newRepo(t, 11, "dependencies-test", nil, []FileChanges{{
		Filename: "go.mod",
		Versions: []string{"module example.com/m\n\ngo 1.22\n"},
	}}, func(_ *user_model.User, repo *repo_model.Repository) {
		deps := []*repo_model.Dependency{
			{RepoID: repo.ID, Ecosystem: "Go", Name: "golang.org/x/net", Version: "v0.17.0", ManifestPath: "go.mod", Direct: true, License: "BSD-3-Clause"},
			{RepoID: repo.ID, Ecosystem: "npm", Name: "express", Version: "4.18.2", ManifestPath: "web/package-lock.json", Direct: true, License: "MIT"},
			{RepoID: repo.ID, Ecosystem: "npm", Name: "body-parser", Version: "1.20.1", ManifestPath: "web/package-lock.json", License: "MIT"},
			{RepoID: repo.ID, Ecosystem: "PyPI", Name: "requests", Version: "2.31.0", ManifestPath: "tools/requirements.txt", Direct: true},
		}
		scan := &repo_model.DependencyScan{
			RepoID: repo.ID, CommitSHA: headCommit(t, repo), ScannedUnix: timeutil.TimeStampNow(), Status: repo_model.DependencyScanOK,
			ManifestCount: 3, DependencyCount: len(deps),
		}
		require.NoError(t, repo_model.ReplaceRepoDependencies(db.DefaultContext, scan, deps))
	})
	// the vulnerability alerts (vulnerability-alerts.test.e2e.ts): two confirmed alerts, one on a range (may be affected)
	newRepo(t, 11, "vulnerability-alerts-test", nil, []FileChanges{{
		Filename: "web/package-lock.json",
		Versions: []string{"{}"},
	}}, func(_ *user_model.User, repo *repo_model.Repository) {
		advisory := func(id, summary, name, fixed, level string) *vulnerability.Advisory {
			return &vulnerability.Advisory{
				ID: id, Summary: summary, Details: "Details of " + id + " with a [link](https://example.org/" + id + ").", Level: level, Modified: time.Unix(1700000000, 0),
				Affected: []vulnerability.Affected{{Name: name, Ranges: []vulnerability.Range{{Type: "SEMVER", Events: []vulnerability.Event{{Introduced: "0"}, {Fixed: fixed}}}}}},
			}
		}
		_, err := vulnerability_model.ImportBatch(db.DefaultContext, "npm", []*vulnerability.Advisory{
			advisory("GHSA-qwcr-r2fm-qrc7", "body-parser vulnerable to denial of service when url encoding is enabled", "body-parser", "1.20.3", vulnerability.SeverityHigh),
			advisory("GHSA-35jh-r3h4-6jhm", "Command Injection in lodash", "lodash", "4.17.21", vulnerability.SeverityCritical),
		}, &vulnerability_model.ChangedSet{})
		require.NoError(t, err)
		deps := []*repo_model.Dependency{
			{RepoID: repo.ID, Ecosystem: "npm", Name: "body-parser", Version: "1.20.1", ManifestPath: "web/package-lock.json", Direct: true},
			{RepoID: repo.ID, Ecosystem: "npm", Name: "lodash", Requirement: "~4.17.20", ManifestPath: "web/package.json", Direct: true},
		}
		scan := &repo_model.DependencyScan{
			RepoID: repo.ID, CommitSHA: headCommit(t, repo), ScannedUnix: timeutil.TimeStampNow(), Status: repo_model.DependencyScanOK,
			ManifestCount: 2, DependencyCount: len(deps),
		}
		require.NoError(t, repo_model.ReplaceRepoDependencies(db.DefaultContext, scan, deps))
		_, err = vulnerability_model.RefreshAlerts(db.DefaultContext, repo.ID, nil)
		require.NoError(t, err)
	})
	// a manifest without a lock file: its direct dependencies with their ranges, not pinned
	newRepo(t, 11, "dependencies-unpinned", nil, []FileChanges{{
		Filename: "web/package.json",
		Versions: []string{`{"dependencies":{"express":"^4.18.0"}}`},
	}}, func(_ *user_model.User, repo *repo_model.Repository) {
		deps := []*repo_model.Dependency{
			{RepoID: repo.ID, Ecosystem: "npm", Name: "express", Requirement: "^4.18.0", ManifestPath: "web/package.json", Direct: true},
			{RepoID: repo.ID, Ecosystem: "npm", Name: "lodash", Requirement: "~4.17.20", ManifestPath: "web/package.json", Direct: true},
		}
		scan := &repo_model.DependencyScan{
			RepoID: repo.ID, CommitSHA: headCommit(t, repo), ScannedUnix: timeutil.TimeStampNow(), Status: repo_model.DependencyScanOK,
			ManifestCount: 1, DependencyCount: len(deps),
		}
		require.NoError(t, repo_model.ReplaceRepoDependencies(db.DefaultContext, scan, deps))
	})
	// a repository whose code unit is off: its readers see the issues but not the code
	newRepo(t, 11, "dependencies-no-code", map[unit_model.Type]convert.Conversion{unit_model.TypeIssues: nil}, []FileChanges{{
		Filename: "go.mod",
		Versions: []string{"module example.com/m\n\ngo 1.22\n"},
	}}, func(_ *user_model.User, repo *repo_model.Repository) {
		_, err := db.Exec(db.DefaultContext, "DELETE FROM repo_unit WHERE repo_id = ? AND type = ?", repo.ID, unit_model.TypeCode)
		require.NoError(t, err)
		scan := &repo_model.DependencyScan{RepoID: repo.ID, CommitSHA: headCommit(t, repo), ScannedUnix: timeutil.TimeStampNow(), Status: repo_model.DependencyScanOK}
		require.NoError(t, repo_model.ReplaceRepoDependencies(db.DefaultContext, scan, []*repo_model.Dependency{}))
	})
	newRepo(t, 2, "language-stats-test", nil, []FileChanges{{
		Filename: "main.rs",
		Versions: []string{"fn main() {", "println!(\"Hello World!\");", "}"},
	}}, nil)
	newRepo(t, 2, "mentions-highlighted", nil, []FileChanges{
		{
			Filename:  "history1.md",
			Versions:  []string{""},
			CommitMsg: "A commit message which mentions @user2 in the title\nand has some additional text which mentions @user1",
		},
		{
			Filename:  "history2.md",
			Versions:  []string{""},
			CommitMsg: "Another commit which mentions @user1 in the title\nand @user2 in the text",
		},
	}, nil)
	newRepo(t, 2, "multiline-commit-messages", nil, []FileChanges{
		{
			Filename:  "file1.md",
			Versions:  []string{"file"},
			CommitMsg: "A commit message\nwhich spans multiple lines",
		},
		{
			Filename:  "file3.md",
			Versions:  []string{"another file"},
			CommitMsg: "a\nb",
		},
		{
			Filename:  "file4.md",
			Versions:  []string{"yet another file"},
			CommitMsg: "Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed\n\ndo eiusmod tempor incididunt ut labore et dolore magna aliqua. Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non proident, sunt in culpa qui officia deserunt mollit anim id est laborum.\n\nDolorem cupiditate deleniti illo quo vitae culpa totam blanditiis. Architecto molestias eveniet quibusdam voluptas saepe modi reprehenderit quos. Nobis qui ipsam id et delectus. Corrupti cupiditate occaecati eius. Voluptas voluptatibus culpa nostrum. Id temporibus minima quis voluptate. Et sit quos autem est natus saepe. Velit vitae eos sint magnam et magnam dolore. Aspernatur suscipit dolorem sint fugiat repudiandae provident dolorem voluptatem. Ullam ut unde aperiam. Aut occaecati sit placeat adipisci non. Animi tempore autem molestias numquam ut qui iste. Pariatur recusandae ipsam maxime nihil quia veniam. Doloremque voluptatibus voluptatum consequatur illum iure aperiam deleniti non. Quo quisquam eveniet nihil animi. Et unde in sint eligendi aut autem. Veniam voluptates debitis ullam doloremque. Debitis provident tempore ab fugiat aut distinctio omnis. Vel sapiente nulla id. Quia aliquam ab est. Consequatur voluptatem id blanditiis distinctio. Qui expedita quibusdam qui earum quis culpa. Iste laudantium fuga vero provident voluptatem laboriosam ullam et. Alias consequuntur earum dolor nemo molestiae non neque.",
		},
	}, func(user *user_model.User, repo *repo_model.Repository) {
		// status+tag on main branch
		commitMainSha1 := commitNewFile(t, user, repo, "Another multiline commit message\nOnly this time, we have a big shiny status icon 🎉", "file1.5.md", "also a file")
		commitMainSha2 := commitNewFile(t, user, repo, "This is a commit.\nThe commit knows where it is because it knows where it isn't.", "file2.md", "also a file")
		addCommitStatus(t, user, repo, repo.DefaultBranch, commitMainSha1)
		addCommitStatus(t, user, repo, repo.DefaultBranch, commitMainSha2)
		tagCommitWithRelease(t, user, repo, commitMainSha2, "v1.4.2")

		// status on PR
		commitPrSha1 := addCommitWithMessageToBranch(t, user, repo, "main", "test-branch", "Yet another multiline commit message\nnow with a PR and status!", "file2.md", "", "still a file")
		commitPrSha2 := addCommitWithMessageToBranch(t, user, repo, "test-branch", "test-branch", "One more message\nThis time, the details are longer than the summary. Imagine that!", "file2.md", commitPrSha1, "still a file")
		commitPrSha3 := addCommitWithMessageToBranch(t, user, repo, "test-branch", "test-branch", "Normal commit message", "file2.md", commitPrSha2, "yep, still a file")
		commitPrSha4 := addCommitWithMessageToBranch(t, user, repo, "test-branch", "test-branch", "Normal commit message with status", "file2.md", commitPrSha3, "yep, still a file")
		addCommitWithMessageToBranch(t, user, repo, "test-branch", "test-branch", "Normal multiline\ncommit message", "file2.md", commitPrSha4, "yep, still a file")
		postPullRequest(repo, "test-branch", user, 455, "pullreq", "PR with multiline commits")
		addCommitStatus(t, user, repo, "test-branch", commitPrSha1)
		addCommitStatus(t, user, repo, "test-branch", commitPrSha2)
		addCommitStatus(t, user, repo, "test-branch", commitPrSha4)
	})
	newRepo(t, 2, "file-uploads", nil, []FileChanges{{
		Filename: "UPLOAD_TEST.md",
		Versions: []string{"# File upload test\nUse this repo to test various file upload features in new branches."},
	}}, nil)
	newRepo(t, 2, "unicode-escaping", map[unit_model.Type]convert.Conversion{
		unit_model.TypeCode: nil,
		unit_model.TypeWiki: nil,
	}, []FileChanges{{
		Filename: "a-file",
		Versions: []string{"{a}{а}"},
	}}, func(user *user_model.User, repo *repo_model.Repository) {
		wiki.InitWiki(db.DefaultContext, repo)
		wiki.AddWikiPage(db.DefaultContext, user, repo, "Home", "{a}{а}", "{a}{а}")
		wiki.AddWikiPage(db.DefaultContext, user, repo, "_Sidebar", "{a}{а}", "{a}{а}")
		wiki.AddWikiPage(db.DefaultContext, user, repo, "_Footer", "{a}{а}", "{a}{а}")
	})
	newRepo(t, 2, "multiple-combo-boxes", nil, []FileChanges{{
		Filename: ".forgejo/issue_template/multi-combo-boxes.yaml",
		Versions: []string{`
name: "Multiple combo-boxes"
description: "To show something"
body:
- type: textarea
  id: textarea-one
  attributes:
    label: one
- type: textarea
  id: textarea-two
  attributes:
    label: two
`},
	}}, nil)
	newRepo(t, 2, "markup-attention", nil, []FileChanges{{
		Filename: "github-modern.md",
		Versions: []string{`
> [!note]
> This text is a note

> [!tip]
> This text is a tip

> [!important]
> This text is important

> [!warning]
> This text is a warning

> [!caution]
> This text is to make someone cautious
`},
	}}, nil)
	newRepo(t, 11, "dependency-test", map[unit_model.Type]convert.Conversion{
		unit_model.TypeIssues: &repo_model.IssuesConfig{
			EnableDependencies: true,
		},
		unit_model.TypeCode: nil,
	}, []FileChanges{}, func(user *user_model.User, repo *repo_model.Repository) {
		postIssue(repo, user, 500, "first issue here", "an issue created earlier")
		postIssue(repo, user, 400, "second issue here", "not the right issue, but in the right repo")
		postIssue(repo, user, 300, "third issue here", "depends on things")
		postIssue(repo, user, 200, "unrelated issue", "shrug emoji")
		postIssue(repo, user, 100, "newest issue", "very new")
	})
	newRepo(t, 11, "dependency-test-2", map[unit_model.Type]convert.Conversion{
		unit_model.TypeIssues: &repo_model.IssuesConfig{
			EnableDependencies: true,
		},
		unit_model.TypeCode: nil,
	}, []FileChanges{}, func(user *user_model.User, repo *repo_model.Repository) {
		postIssue(repo, user, 450, "right issue", "an issue containing word right")
		postIssue(repo, user, 150, "left issue", "an issue containing word left")
	})
	newRepo(t, 2, "long-diff-test", nil, []FileChanges{{
		Filename: "test-README.md",
		Versions: []string{
			readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/0-README.md"),
		},
	}}, func(user *user_model.User, repo *repo_model.Repository) {
		commit1Sha := addCommitToBranch(t, user, repo, "main", "test-branch", "test-README.md", "",
			readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/1-README.md"))
		commit2Sha := addCommitToBranch(t, user, repo, "test-branch", "test-branch", "test-README.md", commit1Sha,
			readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/2-README.md"))
		addCommitToBranch(t, user, repo, "test-branch", "test-branch", "test-README.md", commit2Sha,
			readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/3-README.md"))
	})
	newRepo(t, 2, "huge-diff-test", nil, []FileChanges{{
		Filename: "glossary.po",
		Versions: []string{
			func() string {
				var sb strings.Builder
				sb.Write([]byte("0"))
				for i := 1; i < 2000; i++ {
					sb.WriteString(strconv.Itoa(i))
					sb.WriteByte('\n')
				}
				return sb.String()
			}(),
		},
	}}, func(user *user_model.User, repo *repo_model.Repository) {
		addCommitToBranch(t, user, repo, "main", "main-2", "glossary.po", "",
			func() string {
				var sb strings.Builder
				sb.Write([]byte("0"))
				for i := 1; i < 2000; i++ {
					sb.WriteString(strconv.Itoa(i))
					if i%12 == 0 {
						sb.WriteString("Blub")
					}
					sb.WriteByte('\n')
				}
				return sb.String()
			}())
	})
	newRepo(t, 2, "funding_basic_complete", nil, []FileChanges{{
		Filename: ".forgejo/FUNDING.yml",
		Versions: []string{`
community_bridge: example
github:
  - example
  - example2
issuehunt: example
ko_fi: [example, example_2_electric_boogaloo]
liberapay: example
patreon: example
open_collective: example
buy_me_a_coffee: example
thanks_dev: u/gh/example
tidelift: npm/example
custom: ["https://example.com", 😀.com]
`},
	}}, nil)
	newRepo(t, 2, "funding_some_valid", nil, []FileChanges{{
		Filename: ".forgejo/FUNDING.yml",
		Versions: []string{`
ko_fi: 1337
custom: ["https://example.com"]
ko-fi: example
`},
	}}, nil)
	newRepo(t, 2, "funding_with_a_really_ridiculously_long_title_that_doesnt_really_happen_all_that_often_normally_but_could_really_mess_with_things_if_not_handled_properly", nil, []FileChanges{{
		Filename: ".forgejo/FUNDING.yml",
		Versions: []string{`
custom: example.com
`},
	}}, nil)
	newRepo(t, 6, ".profile", nil, []FileChanges{{
		Filename: ".forgejo/FUNDING.yml",
		Versions: []string{`
ko_fi: example
liberapay: example
custom: "http://localhost:3003/"
`},
	}}, nil)
	newRepo(t, 39, ".profile", nil, []FileChanges{{
		Filename: "FUNDING.yml",
		Versions: []string{`
ko_fi: example
liberapay: example
custom: "http://localhost:3003/"
`},
	}}, nil)
	newRepo(t, 2, "drawio-test", nil, []FileChanges{
		{
			Filename: "docs/arch.drawio",
			Versions: []string{`<mxfile><diagram id="d" name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/>` +
				`<mxCell id="2" value="Hello drawio" style="rounded=0;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="40" y="40" width="120" height="60" as="geometry"/></mxCell>` +
				`<mxCell id="3" value="&lt;img src=x onerror=&quot;document.title='pwned'&quot;&gt;xss" style="html=1;" vertex="1" parent="1"><mxGeometry x="200" y="40" width="120" height="60" as="geometry"/></mxCell>` +
				`<mxCell id="4" value="" style="shape=image;image=https://example.com/x.png;" vertex="1" parent="1"><mxGeometry x="360" y="40" width="60" height="60" as="geometry"/></mxCell>` +
				`</root></mxGraphModel></diagram></mxfile>`},
		},
		{
			Filename: "arch.drawio.svg",
			Versions: []string{`<svg xmlns="http://www.w3.org/2000/svg" width="120" height="60"><rect width="120" height="60" fill="#ccc"/></svg>`},
		},
		{
			Filename: "diagram.md",
			Versions: []string{"![arch](arch.drawio.svg)\n"},
		},
	}, nil)
	newRepo(t, 2, "actions-graph-test", map[unit_model.Type]convert.Conversion{
		unit_model.TypeCode:    nil,
		unit_model.TypeActions: &repo_model.ActionsConfig{},
	}, nil, func(user *user_model.User, repo *repo_model.Repository) {
		// The jobs stay unscheduled (no runner in e2e): "build" waits and the rest is blocked on it.
		workflows, err := jobparser.Parse([]byte(`
name: graph
on: push
jobs:
  build:
    runs-on: docker
    steps:
      - run: true
  test:
    needs: [build]
    runs-on: docker
    strategy:
      matrix:
        shard: [a, b]
    steps:
      - run: true
  deploy:
    needs: [test]
    runs-on: docker
    steps:
      - run: true
`), false)
		require.NoError(t, err)
		require.NoError(t, actions_service.InsertRun(db.DefaultContext, &actions_model.ActionRun{
			Title:         "graph run",
			RepoID:        repo.ID,
			OwnerID:       user.ID,
			WorkflowID:    "graph.yml",
			TriggerUserID: user.ID,
			Ref:           "refs/heads/main",
			Event:         "push",
			EventPayload:  "{}",
			Status:        actions_model.StatusWaiting,
		}, workflows))
	})
	newRepo(t, 2, "permalink-test", nil, []FileChanges{
		{Filename: "a.txt", Versions: []string{"one\ntwo\nthree\n"}},
		// tall enough to push the comments below it when it finishes loading
		{Filename: "tall.svg", Versions: []string{`<svg xmlns="http://www.w3.org/2000/svg" width="400" height="500"><rect width="400" height="500" fill="#ccc"/></svg>`}},
	}, func(user *user_model.User, repo *repo_model.Repository) {
		sha := addCommitToBranch(t, user, repo, "main", "feat", "a.txt", "", "one\nTWO\nthree\n")
		postPullRequest(repo, "feat", user, 100, "Many comments", "PR for permalink e2e")
		issue, err := issues_model.GetIssueByIndex(db.DefaultContext, repo.ID, 1)
		require.NoError(t, err)
		require.NoError(t, issue.LoadPullRequest(db.DefaultContext))

		// A code comment on line 2, made outdated by a later commit on the head branch.
		reviewer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
		gitRepo, err := gitrepo.OpenRepository(db.DefaultContext, repo)
		require.NoError(t, err)
		defer gitRepo.Close()
		codeComment, err := pull_service.CreateCodeComment(db.DefaultContext, reviewer, gitRepo, issue, 2, 0, "OUTDATED-TARGET", "a.txt", false, 0, "", sha, nil)
		require.NoError(t, err)
		_, err = db.GetEngine(db.DefaultContext).ID(codeComment.ID).Cols("invalidated").Update(&issues_model.Comment{Invalidated: true})
		require.NoError(t, err)

		// Images, math and mermaid come before the target, so their late rendering shifts the layout.
		const total, target = 520, 450
		for i := range total {
			content := fmt.Sprintf("comment %d", i)
			switch {
			case i == target:
				content = "PERMALINK-TARGET"
			case i < target && i%5 == 1:
				content += fmt.Sprintf("\n\n![img](/user2/permalink-test/raw/branch/main/tall.svg?n=%d)", i)
			case i < target && i%5 == 2:
				content += "\n\n$x^2 + \\sum_i i$"
			case i < target && i%5 == 3:
				content += "\n\n```mermaid\ngraph TD; A-->B;\n```"
			}
			// plain rows: the service layer (notifications, indexers) costs about 60 ms per comment
			require.NoError(t, db.Insert(db.DefaultContext, &issues_model.Comment{Type: issues_model.CommentTypeComment, PosterID: user.ID, IssueID: issue.ID, Content: content}))
		}
	})
	newRepo(t, 2, "pull-metrics", nil, []FileChanges{{Filename: "metrics.md", Versions: []string{"metrics"}}}, func(user *user_model.User, repo *repo_model.Repository) {
		forgery.EnableRepoUnits(t, repo, unit_model.TypePullRequests)
		// plain rows with known times: pull request i opened as a draft, made ready, reviewed and merged (but the last)
		ctx := db.DefaultContext
		const hour = 3600
		for i := range int64(6) {
			created := now.Add(-(i*4 + 3) * 24 * hour)
			ready, reviewed, merged := created.Add(i*2*hour), created.Add(i*2*hour+(i+1)*5*hour), created.Add(i*2*hour+36*hour)
			issue := &issues_model.Issue{RepoID: repo.ID, Index: i + 1, PosterID: user.ID, Title: fmt.Sprintf("Metrics %d", i+1), IsPull: true}
			require.NoError(t, db.Insert(ctx, issue))
			require.NoError(t, db.Insert(ctx, &issues_model.PullRequest{IssueID: issue.ID, Index: issue.Index, BaseRepoID: repo.ID, HeadRepoID: repo.ID, HasMerged: i < 5, CommitsAhead: int(i + 1)}))
			change := &issues_model.Comment{Type: issues_model.CommentTypeChangeTitle, PosterID: user.ID, IssueID: issue.ID, OldTitle: "WIP: " + issue.Title, NewTitle: issue.Title}
			review := &issues_model.Comment{Type: issues_model.CommentTypeReview, PosterID: 1 + i%2*3, IssueID: issue.ID}
			require.NoError(t, db.Insert(ctx, change, review))
			for _, q := range []struct {
				sql string
				at  timeutil.TimeStamp
				id  int64
			}{
				{"UPDATE issue SET created_unix = ? WHERE id = ?", created, issue.ID},
				{"UPDATE comment SET created_unix = ? WHERE id = ?", ready, change.ID},
				{"UPDATE comment SET created_unix = ? WHERE id = ?", reviewed, review.ID},
				{"UPDATE pull_request SET merged_unix = ? WHERE issue_id = ?", merged, issue.ID},
			} {
				_, err := db.GetEngine(ctx).Exec(q.sql, q.at, q.id)
				require.NoError(t, err)
			}
		}
	})
	// add your repo declarations here
}

func readStringFile(t *testing.T, fn string) string {
	c, err := os.ReadFile(filepath.Join(setting.AppWorkPath, fn))
	require.NoError(t, err)
	return string(c)
}

func newRepo(t *testing.T, userID int64, repoName string, enabledUnits map[unit_model.Type]convert.Conversion, fileChanges []FileChanges, setup SetupRepo) {
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: userID})

	somerepo := forgery.CreateRepository(t, user, &forgery.CreateRepositoryOptions{
		Name:  repoName,
		Files: forgery.FilesInit{},
	})
	if len(enabledUnits) == 0 {
		forgery.EnableRepoUnits(t, somerepo,
			unit_model.TypeCode,
			unit_model.TypeIssues,
		)
	}
	for unit, config := range enabledUnits {
		forgery.EnableRepoUnit(t, somerepo, unit, config)
	}

	var lastCommitID string
	for _, file := range fileChanges {
		for i, version := range file.Versions {
			operation := "update"
			if i == 0 {
				operation = "create"
			}

			// default to unique commit messages
			commitMsg := file.CommitMsg
			if commitMsg == "" {
				commitMsg = fmt.Sprintf("Patch: %s-%d", file.Filename, i+1)
			}

			resp, err := files_service.ChangeRepoFiles(git.DefaultContext, somerepo, user, &files_service.ChangeRepoFilesOptions{
				Files: []*files_service.ChangeRepoFile{{
					Operation:     operation,
					TreePath:      file.Filename,
					ContentReader: strings.NewReader(version),
				}},
				Message:   commitMsg,
				OldBranch: "main",
				NewBranch: "main",
				Author: &files_service.IdentityOptions{
					Name:  user.Name,
					Email: user.Email,
				},
				Committer: &files_service.IdentityOptions{
					Name:  user.Name,
					Email: user.Email,
				},
				Dates: &files_service.CommitDateOptions{
					Author:    time.Now(),
					Committer: time.Now(),
				},
				LastCommitID: lastCommitID,
			})
			require.NoError(t, err)
			assert.NotEmpty(t, resp)

			lastCommitID = resp.Commit.SHA
		}
	}

	if setup != nil {
		setup(user, somerepo)
	}

	err := stats.UpdateRepoIndexer(somerepo)
	require.NoError(t, err)
}

func addCommitToBranch(t *testing.T, user *user_model.User, repo *repo_model.Repository, oldBranch, newBranch, filename, lastSha, content string) string {
	return addCommitWithMessageToBranch(t, user, repo, oldBranch, newBranch, "add commit to branch", filename, lastSha, content)
}

func addCommitWithMessageToBranch(t *testing.T, user *user_model.User, repo *repo_model.Repository, oldBranch, newBranch, commitMessage, filename, lastSha, content string) string {
	resp, err := files_service.ChangeRepoFiles(git.DefaultContext, repo, user, &files_service.ChangeRepoFilesOptions{
		Files: []*files_service.ChangeRepoFile{{
			Operation:     "update",
			TreePath:      filename,
			ContentReader: strings.NewReader(content),
		}},
		Message:   commitMessage,
		OldBranch: oldBranch,
		NewBranch: newBranch,
		Author: &files_service.IdentityOptions{
			Name:  user.Name,
			Email: user.Email,
		},
		Committer: &files_service.IdentityOptions{
			Name:  user.Name,
			Email: user.Email,
		},
		Dates: &files_service.CommitDateOptions{
			Author:    time.Now(),
			Committer: time.Now(),
		},
		LastCommitID: lastSha,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
	return resp.Commit.SHA
}

func commitNewFile(t *testing.T, user *user_model.User, repo *repo_model.Repository, commitMessage, filename, content string) string {
	resp, err := files_service.ChangeRepoFiles(git.DefaultContext, repo, user, &files_service.ChangeRepoFilesOptions{
		Files: []*files_service.ChangeRepoFile{{
			Operation:     "create",
			TreePath:      filename,
			ContentReader: strings.NewReader(content),
		}},
		Message: commitMessage,
		Author: &files_service.IdentityOptions{
			Name:  user.Name,
			Email: user.Email,
		},
		Committer: &files_service.IdentityOptions{
			Name:  user.Name,
			Email: user.Email,
		},
		Dates: &files_service.CommitDateOptions{
			Author:    time.Now(),
			Committer: time.Now(),
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
	return resp.Commit.SHA
}

func addCommitStatus(t *testing.T, user *user_model.User, repo *repo_model.Repository, branchName, commitSha string) {
	run := &actions_model.ActionRun{
		ID:                rand.Int63(),
		Title:             "test action",
		RepoID:            repo.ID,
		OwnerID:           user.ID,
		WorkflowID:        "",
		WorkflowDirectory: "",
		TriggerUserID:     user.ID,
		Ref:               "refs/heads/" + branchName,
		Event:             "push",
		EventPayload:      `{"head_commit": {"id": "` + commitSha + `"}}`,
		CommitSHA:         commitSha,
		Status:            actions_model.StatusSuccess,
	}
	err := actions_service.InsertRun(db.DefaultContext, run, []*jobparser.SingleWorkflow{})
	require.NoError(t, err)
	job := &actions_model.ActionRunJob{
		ID:                rand.Int63(),
		RunID:             run.ID,
		Attempt:           1,
		Status:            actions_model.StatusSuccess,
		RepoID:            run.RepoID,
		OwnerID:           run.OwnerID,
		CommitSHA:         commitSha,
		IsForkPullRequest: false,
	}
	actions_service.CreateCommitStatus(db.DefaultContext, job)
}

func tagCommitWithRelease(t *testing.T, user *user_model.User, repo *repo_model.Repository, target, tagName string) {
	gitRepo, err := gitrepo.OpenRepository(db.DefaultContext, repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	rel := &repo_model.Release{
		RepoID:           repo.ID,
		PublisherID:      user.ID,
		Publisher:        user,
		TagName:          tagName,
		Target:           target,
		Title:            tagName,
		Note:             "",
		IsDraft:          false,
		IsPrerelease:     false,
		HideArchiveLinks: false,
		IsTag:            false,
		Repo:             repo,
	}
	err = release_service.CreateRelease(gitRepo, rel, "", nil)
	require.NoError(t, err)
}

// headCommit is the commit the default branch of the repository points to. A stored scan must name it: a page whose
// scan names another commit queues a new scan, which would replace the rows the spec expects.
func headCommit(t *testing.T, repo *repo_model.Repository) string {
	t.Helper()
	gitRepo, err := gitrepo.OpenRepository(t.Context(), repo)
	require.NoError(t, err)
	defer gitRepo.Close()
	sha, err := gitRepo.GetBranchCommitID(repo.DefaultBranch)
	require.NoError(t, err)
	return sha
}
