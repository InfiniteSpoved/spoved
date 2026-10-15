// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	audit_service "forgejo.org/services/audit"

	"github.com/urfave/cli/v3"
)

// auditFlag is the flag of a filter parameter: before is --until, the rest use dashes.
func auditFlag(param string) string {
	if param == "before" {
		return "until"
	}
	return strings.ReplaceAll(param, "_", "-")
}

func subcmdAudit() *cli.Command {
	flags := []cli.Flag{
		&cli.StringFlag{Name: "format", Value: "csv", Usage: "csv or json"},
		&cli.StringFlag{Name: "output", Usage: "write to this new file (mode 0600) instead of stdout"},
	}
	for _, p := range audit_service.Params {
		flags = append(flags, &cli.StringFlag{Name: auditFlag(p), Usage: "filter: the " + p + " parameter of /admin/audit"})
	}
	return &cli.Command{
		Name:  "audit",
		Usage: "Read the audit log",
		Commands: []*cli.Command{
			{
				Name:   "export",
				Usage:  "Export the audit log, with no row limit; the manifest goes to stderr",
				Before: noDanglingArgs,
				Action: runAuditExport,
				Flags:  flags,
			},
		},
	}
}

func runAuditExport(ctx context.Context, c *cli.Command) error {
	ctx, cancel := installSignals(ctx)
	defer cancel()

	format := c.String("format")
	if format != "csv" && format != "json" {
		return fmt.Errorf("--format must be csv or json")
	}
	if err := initDB(ctx); err != nil {
		return err
	}
	q := url.Values{}
	for _, p := range audit_service.Params {
		q.Set(p, c.String(auditFlag(p)))
	}
	f, err := audit_service.ParseFilter(ctx, q.Get)
	if err != nil {
		return err
	}

	var out io.Writer = os.Stdout
	if path := c.String("output"); path != "" {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		out = file
	}
	w := bufio.NewWriter(out)
	m, err := audit_service.Export(ctx, w, f, format, 0, func(*audit_service.Manifest) error { return nil })
	if err == nil {
		err = w.Flush()
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(os.Stderr, "X-Audit-Filter: %s\nX-Audit-Generated-At: %s\nX-Audit-Truncated: false\nRows: %d\n",
		m.FilterHeader(), m.GeneratedAt.Format(time.RFC3339), m.Rows)
	return err
}
