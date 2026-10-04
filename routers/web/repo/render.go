// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"path"

	"forgejo.org/modules/charset"
	"forgejo.org/modules/git"
	"forgejo.org/modules/log"
	"forgejo.org/modules/markup"
	"forgejo.org/modules/markup/drawio"
	"forgejo.org/modules/setting"
	"forgejo.org/services/context"
)

// RenderFile renders a file by repos path
func RenderFile(ctx *context.Context) {
	blob, err := ctx.Repo.Commit.GetBlobByPath(ctx.Repo.TreePath)
	if err != nil {
		if git.IsErrNotExist(err) {
			ctx.NotFound("GetBlobByPath", err)
		} else {
			ctx.ServerError("GetBlobByPath", err)
		}
		return
	}

	buf, dataRc, fInfo, err := getFileReader(ctx, ctx.Repo.Repository.ID, blob)
	if err != nil {
		ctx.ServerError("getFileReader", err)
		return
	}
	defer dataRc.Close()

	rd := charset.ToUTF8WithFallbackReader(io.MultiReader(bytes.NewReader(buf), dataRc), charset.ConvertOpts{})
	ctx.Resp.Header().Add("Content-Security-Policy", "frame-src 'self'; sandbox allow-scripts")

	markupType := markup.Type(blob.Name())
	if markupType == (drawio.Renderer{}).Name() {
		// keep the draw.io viewer from fetching anything outside Forgejo
		scriptSrc := "'self' 'unsafe-inline'"
		if u, err := url.Parse(setting.StaticURLPrefix); err == nil && u.Host != "" {
			scriptSrc += " " + u.Scheme + "://" + u.Host
		}
		ctx.Resp.Header().Add("Content-Security-Policy", "default-src 'none'; script-src "+scriptSrc+"; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:")
	}
	if markupType == "" {
		if fInfo.isTextFile {
			_, _ = io.Copy(ctx.Resp, rd)
		} else {
			http.Error(ctx.Resp, "Unsupported file type render", http.StatusInternalServerError)
		}
		return
	}

	err = markup.Render(&markup.RenderContext{
		Ctx:          ctx,
		RelativePath: ctx.Repo.TreePath,
		Links: markup.Links{
			Base:       ctx.Repo.RepoLink,
			BranchPath: ctx.Repo.BranchNameSubURL(),
			TreePath:   path.Dir(ctx.Repo.TreePath),
		},
		Metas:            ctx.Repo.Repository.ComposeDocumentMetas(ctx),
		GitRepo:          ctx.Repo.GitRepo,
		InStandalonePage: true,
	}, rd, ctx.Resp)
	if err != nil {
		log.Error("Failed to render file %q: %v", ctx.Repo.TreePath, err)
		http.Error(ctx.Resp, "Failed to render file", http.StatusInternalServerError)
		return
	}
}
