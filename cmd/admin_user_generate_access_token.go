// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"

	auth_model "forgejo.org/models/auth"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/timeutil"
	"forgejo.org/services/authz"

	"github.com/urfave/cli/v3"
)

func microcmdUserGenerateAccessToken() *cli.Command {
	return &cli.Command{
		Name:  "generate-access-token",
		Usage: "Generate an access token for a specific user",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "username",
				Aliases: []string{"u"},
				Usage:   "Username",
			},
			&cli.StringFlag{
				Name:    "token-name",
				Aliases: []string{"t"},
				Usage:   "Token name",
				Value:   "gitea-admin",
			},
			&cli.BoolFlag{
				Name:  "raw",
				Usage: "Display only the token value",
			},
			&cli.StringFlag{
				Name:  "scopes",
				Value: "all",
				Usage: `Comma separated list of scopes to apply to access token, examples: "all", "public-only,read:issue", "write:repository,write:user"`,
			},
			&cli.StringFlag{
				Name:  "expires-at",
				Usage: "Expiry date of the token (YYYY-MM-DD); default: the latest date the [security] ACCESS_TOKEN_* policy allows",
			},
		},
		Before: noDanglingArgs,
		Action: runGenerateAccessToken,
	}
}

func runGenerateAccessToken(ctx context.Context, c *cli.Command) error {
	if !c.IsSet("username") {
		return errors.New("you must provide a username to generate a token for")
	}

	ctx, cancel := installSignals(ctx)
	defer cancel()

	if err := initDB(ctx); err != nil {
		return err
	}

	user, err := user_model.GetUserByName(ctx, c.String("username"))
	if err != nil {
		return err
	}

	// construct token with name and user so we can make sure it is unique
	t := &auth_model.AccessToken{
		Name: c.String("token-name"),
		UID:  user.ID,
	}

	exist, err := auth_model.AccessTokenByNameExists(ctx, t)
	if err != nil {
		return err
	}
	if exist {
		return errors.New("access token name has been used already")
	}

	// make sure the scopes are valid
	accessTokenScope, err := auth_model.AccessTokenScope(c.String("scopes")).Normalize()
	if err != nil {
		return fmt.Errorf("invalid access token scope provided: %w", err)
	}
	if !accessTokenScope.HasPermissionScope() {
		return errors.New("access token does not have any permission")
	}
	t.Scope = accessTokenScope
	if t.ExpiresUnix, err = cliTokenExpiry(c.String("expires-at")); err != nil {
		return err
	}

	// maintain legacy behaviour until new CLI options are added -- token has access to all resources, is not
	// fine-grained
	t.ResourceAllRepos = true

	// create the token
	if err := auth_model.NewAccessToken(ctx, t, nil); err != nil {
		return err
	}

	if c.Bool("raw") {
		fmt.Printf("%s\n", t.Token)
	} else {
		fmt.Printf("Access token was successfully created: %s\n", t.Token)
	}

	return nil
}

// cliTokenExpiry parses an expiry date given on the command line and checks it against the access token policy.
// Without one, the token gets the latest date the policy allows (none when it has no maximum).
func cliTokenExpiry(value string) (timeutil.TimeStamp, error) {
	expires, err := authz.ParseExpiryDate(value)
	if err != nil {
		return 0, err
	}
	if expires == 0 {
		expires = authz.MaxExpiry(setting.AccessTokenExpiry)
	}
	return expires, authz.ValidateExpiry(setting.AccessTokenExpiry, expires)
}
