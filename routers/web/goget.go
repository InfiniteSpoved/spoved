// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"strings"

	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/services/context"
)

func goGet(ctx *context.Context) {
	if ctx.Req.Method != "GET" || len(ctx.Req.URL.RawQuery) < 8 || ctx.FormString("go-get") != "1" {
		return
	}

	parts := strings.SplitN(ctx.Req.URL.EscapedPath(), "/", 4)

	if len(parts) < 3 {
		return
	}

	ownerName := parts[1]
	repoName := parts[2]

	// Quick responses appropriate go-get meta with status 200
	// regardless of if user have access to the repository,
	// or the repository does not exist at all.
	// This is particular a workaround for "go get" command which does not respect
	// .netrc file.

	trimmedRepoName := strings.TrimSuffix(repoName, ".git")

	if ownerName == "" || trimmedRepoName == "" {
		_, _ = ctx.Write([]byte(`<!doctype html>
<html>
	<body>
		invalid import path
	</body>
</html>
`))
		ctx.Status(http.StatusBadRequest)
		return
	}
	branchName := setting.Repository.DefaultBranch

	// a repository in a project that the viewer can see is answered as owner/project/repo; anything else (hidden,
	// missing, a root repository) gets the first two segments, whether or not they name a project
	groupRepo := goGetGroupRepository(ctx)
	repo := groupRepo
	if repo == nil {
		repo, _ = repo_model.GetRepositoryByOwnerAndName(ctx, ownerName, repoName)
	}
	if repo != nil && len(repo.DefaultBranch) > 0 {
		branchName = repo.DefaultBranch
	}
	prefix := setting.AppURL + path.Join(url.PathEscape(ownerName), url.PathEscape(repoName), "src", "branch", util.PathEscapeSegments(branchName))
	if groupRepo != nil {
		prefix = groupRepo.HTMLURL() + "/src/branch/" + util.PathEscapeSegments(branchName)
	}

	appURL, _ := url.Parse(setting.AppURL)

	insecure := ""
	if appURL.Scheme == string(setting.HTTP) {
		insecure = "--insecure "
	}

	goGetImport := context.ComposeGoGetImport(ownerName, trimmedRepoName)

	var cloneURL string
	if setting.Repository.GoGetCloneURLProtocol == "ssh" {
		cloneURL = repo_model.ComposeSSHCloneURL(ownerName, repoName)
	} else {
		cloneURL = repo_model.ComposeHTTPSCloneURL(ownerName, repoName)
	}
	if groupRepo != nil {
		goGetImport = path.Join(context.ComposeGoGetImport(groupRepo.OwnerName, groupRepo.GroupName), url.PathEscape(groupRepo.Name))
		if setting.Repository.GoGetCloneURLProtocol == "ssh" {
			cloneURL = groupRepo.CloneLink().SSH
		} else {
			cloneURL = groupRepo.CloneLink().HTTPS
		}
	}
	goImportContent := fmt.Sprintf("%s git %s", goGetImport, cloneURL /*CloneLink*/)
	goSourceContent := fmt.Sprintf("%s _ %s %s", goGetImport, prefix+"{/dir}" /*GoDocDirectory*/, prefix+"{/dir}/{file}#L{line}" /*GoDocFile*/)
	goGetCli := fmt.Sprintf("go get %s%s", insecure, goGetImport)

	res := fmt.Sprintf(`<!doctype html>
<html>
	<head>
		<meta name="go-import" content="%s">
		<meta name="go-source" content="%s">
	</head>
	<body>
		%s
	</body>
</html>`, html.EscapeString(goImportContent), html.EscapeString(goSourceContent), html.EscapeString(goGetCli))

	ctx.RespHeader().Set("Content-Type", "text/html")
	_, _ = ctx.Write([]byte(res))
}

// goGetGroupRepository returns the repository in a project named by the first three segments of a go-get path, if
// the viewer can see it.
func goGetGroupRepository(ctx *context.Context) *repo_model.Repository {
	parts := strings.SplitN(ctx.Req.URL.EscapedPath(), "/", 5)
	if len(parts) < 4 {
		return nil
	}
	names := make([]string, 3)
	for i := range names {
		name, err := url.PathUnescape(parts[i+1])
		if err != nil || name == "" {
			return nil
		}
		names[i] = name
	}
	owner, err := user_model.GetUserByName(ctx, names[0])
	if err != nil {
		return nil
	}
	repo, err := ctx.VisibleGroupRepository(owner, names[1], strings.TrimSuffix(names[2], ".git"))
	if err != nil {
		if !repo_model.IsErrRepoNotExist(err) {
			log.Error("VisibleGroupRepository: %v", err)
		}
		return nil
	}
	return repo
}
