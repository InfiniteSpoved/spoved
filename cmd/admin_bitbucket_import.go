// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package cmd

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"forgejo.org/modules/json"
	"forgejo.org/modules/private"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/services/migrations"

	"github.com/urfave/cli/v3"
)

func subcmdBitbucketImport() *cli.Command {
	return &cli.Command{
		Name:  "bitbucket-import",
		Usage: "Import the projects and repositories of a Bitbucket Data Center server into an organization",
		Description: "The access token is read from the file given with --token-file, or else from the BITBUCKET_TOKEN " +
			"environment variable. The import runs in the server as --doer, a site admin or an owner of --org.",
		Before: noDanglingArgs,
		Action: runBitbucketImport,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "server", Usage: "Base URL of the Bitbucket server, e.g. https://bitbucket.example.com", Required: true},
			&cli.StringFlag{Name: "org", Usage: "Organization to import into", Required: true},
			&cli.StringFlag{Name: "doer", Usage: "User the import runs as", Required: true},
			&cli.StringSliceFlag{Name: "project", Usage: "Key of a project to import (repeatable); every project the token can read if absent"},
			&cli.BoolFlag{Name: "dry-run", Usage: "List what would be imported and change nothing"},
			&cli.BoolFlag{Name: "no-pull-requests", Usage: "Don't import pull requests, their comments and reviews"},
			&cli.StringFlag{Name: "csv", Usage: "Write the old and new URLs of the repositories to this CSV file"},
			&cli.StringFlag{Name: "username", Usage: "Account of a user access token; empty for a project or repository token"},
			&cli.StringFlag{Name: "token-file", Usage: "File holding the Bitbucket access token"},
		},
	}
}

func runBitbucketImport(ctx context.Context, c *cli.Command) error {
	ctx, cancel := installSignals(ctx)
	defer cancel()
	setting.MustInstalled()

	token, err := bitbucketToken(c.String("token-file"))
	if err != nil {
		return err
	}
	text, extra := private.BitbucketImport(ctx, private.BitbucketImportParams{
		Doer:         c.String("doer"),
		Org:          c.String("org"),
		Server:       c.String("server"),
		Username:     c.String("username"),
		Token:        token,
		Projects:     c.StringSlice("project"),
		DryRun:       c.Bool("dry-run"),
		PullRequests: !c.Bool("no-pull-requests"),
	})
	if extra.HasError() {
		return handleCliResponseExtra(extra)
	}
	var report migrations.BitbucketImportReport
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		return err
	}
	printBitbucketReport(c.Root().Writer, &report)
	if path := c.String("csv"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := writeBitbucketCSV(f, &report); err != nil {
			return err
		}
	}
	if report.Blocked {
		return errors.New("nothing was imported: resolve the conflicts above and run again")
	}
	return nil
}

// bitbucketToken reads the token from the file, or else from BITBUCKET_TOKEN: never from the command line.
func bitbucketToken(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	if token := os.Getenv("BITBUCKET_TOKEN"); token != "" {
		return token, nil
	}
	return "", errors.New("set BITBUCKET_TOKEN or --token-file")
}

func printBitbucketReport(w io.Writer, report *migrations.BitbucketImportReport) {
	for _, item := range report.Items {
		line := fmt.Sprintf("%-10s %-10s %s -> %s", item.Kind, item.Status, item.Bitbucket, item.Name)
		if item.NewHTTP != "" {
			line += " (" + item.NewHTTP + ")"
		}
		if item.Reason != "" {
			line += ": " + item.Reason
		}
		_, _ = fmt.Fprintln(w, util.StripControl(line))
	}
}

func writeBitbucketCSV(w io.Writer, report *migrations.BitbucketImportReport) error {
	out := csv.NewWriter(w)
	_ = out.Write([]string{"kind", "bitbucket", "name", "status", "old_http", "old_ssh", "old_web", "new_https", "new_ssh", "new_web"})
	for _, item := range report.Items {
		row := []string{item.Kind, item.Bitbucket, item.Name, item.Status, item.OldHTTP, item.OldSSH, item.OldWeb, item.NewHTTP, item.NewSSH, item.NewWeb}
		for i := range row {
			row[i] = util.CSVSafe(util.StripControl(row[i]))
		}
		_ = out.Write(row)
	}
	out.Flush()
	return out.Error()
}
