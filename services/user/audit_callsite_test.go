// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package user

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auditedUserColumns are the user columns whose changes the audit log must see. rands is not one of them: it only
// salts the activation and reset tokens (updateActivation writes it with every email activation).
var auditedUserColumns = []string{
	"is_active", "prohibit_login", "is_admin", "is_auditor", "is_restricted", "passwd", "must_change_password", "email",
	"login_source", "login_name", "login_type", "visibility",
}

// auditedWrites are the functions allowed to write those columns, or to delete keys, key selections or access tokens,
// with why.
// A new write elsewhere fails TestAuditCallSites: record it in its function, or add it here with a reason.
var auditedWrites = map[string]string{
	// user columns
	"services/user.UpdateUser":                       "records account.user.updated",
	"services/user.UpdateAuth":                       "records account.user.password_changed and account.user.updated",
	"services/user.deleteUserAccount":                "records account.user.purge_started",
	"services/user.tombstoneUser":                    "part of account.user.deleted (tombstone)",
	"services/user.AdminAddOrSetPrimaryEmailAddress": "records account.email.*",
	"services/user.ReplacePrimaryEmailAddress":       "records account.email.*",
	"services/user.MakeEmailAddressPrimary":          "records account.email.primary",
	"services/user.ActivateAccount":                  "records account.user.activated",
	"models/user.ActivateUserEmail":                  "records account.email.* and account.user.updated",
	"models/user.DeletePrimaryEmailAddressOfUser":    "records account.email.deleted (organizations only)",
	"models/user.SetMustChangePassword":              "records account.user.must_change_password",
	"services/usergroup.syncRoles":                   "records account.user.role_granted|revoked",
	"services/remote.MaybePromoteRemoteUser":         "records account.user.updated",
	"services/auth/source/db.Authenticate":           "not recorded: the password rehash at sign-in keeps the same password",
	"models/actions.NewRunnerToken":                  "not the user table (a runner token's is_active)",

	// SSH keys and access tokens
	"services/user.deleteUser":                     "counted in account.user.deleted (bulk, with the account)",
	"models/auth.DeleteAccessTokenByID":            "records credential.access_token.deleted",
	"models/asymkey.deleteKeysMarkedForDeletion":   "records credential.ssh_key.deleted (login source sync)",
	"services/asymkey.DeletePublicKey":             "records credential.ssh_key.deleted",
	"models/asymkey.deleteGPGKey":                  "its one caller DeleteGPGKey records credential.gpg_key.deleted",
	"models.DeleteDeployKey":                       "records credential.deploy_key.deleted",
	"models/asymkey.DeleteOrgDeployKey":            "records credential.org_deploy_key|project_deploy_key.deleted",
	"models/asymkey.SetOrgDeployKeyRepos":          "records credential.org_deploy_key.updated",
	"models/asymkey.RemoveOrgDeployKeyRepo":        "records credential.org_deploy_key.updated",
	"services/repository.DeleteRepositoryDirectly": "selection rows of the deleted repository: part of the repository delete (settings audit)",
	"services/repository.transferOwnership":        "selection rows of the moved repository: part of the transfer (settings audit)",
}

// TestAuditCallSites walks the code for writes of the audited user columns (UpdateUserCols, and Update/Exec chains on
// the user table) and for deletes of SSH keys and access tokens, and fails on one in a function not listed in
// auditedWrites.
func TestAuditCallSites(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]token.Position{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "web_src", "public", "tests", "vendor",
				"gitea_migrations", "forgejo_migrations", "forgejo_migrations_legacy": // schema migrations run before any account exists
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil //nolint:nilerr // not our concern here
		}
		pkg, _ := filepath.Rel(root, filepath.Dir(path))
		pkg = filepath.ToSlash(pkg)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := pkg + "." + fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				name = pkg + ".(" + types(fn.Recv.List[0].Type) + ")." + fn.Name.Name
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && isAuditedWrite(call) {
					if _, seen := found[name]; !seen {
						found[name] = fset.Position(call.Pos())
					}
				}
				return true
			})
		}
		return nil
	}))

	for name, pos := range found {
		_, ok := auditedWrites[name]
		assert.True(t, ok, "%s (%s) writes an audited user column or deletes keys/tokens outside a recorded function", name, pos)
	}
	for name := range auditedWrites {
		_, ok := found[name]
		assert.True(t, ok, "%s is listed in auditedWrites but writes nothing audited: remove it", name)
	}
}

func types(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.StarExpr:
		return "*" + types(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return types(e.X)
	}
	return "?"
}

// isAuditedWrite reports whether call writes an audited user column or deletes SSH keys or access tokens.
func isAuditedWrite(call *ast.CallExpr) bool {
	name := callName(call.Fun)
	switch name {
	case "UpdateUserCols":
		// a column list that isn't all literals (a variable, a spread) counts: the test can't see what it writes
		return slices.ContainsFunc(literals(call), isAuditedColumn) || len(call.Args) > 2 && slices.ContainsFunc(call.Args[2:], func(a ast.Expr) bool {
			_, lit := a.(*ast.BasicLit)
			return !lit
		}) || call.Ellipsis.IsValid()
	case "Update", "Exec":
		var lits []string
		userTable := false
		for c := call; c != nil; c = receiverCall(c) {
			for _, l := range literals(c) {
				lits = append(lits, l)
				lower := strings.ToLower(l)
				if callName(c.Fun) == "Table" && strings.Trim(lower, "`\"") == "user" ||
					strings.Contains(lower, "update `user`") || strings.Contains(lower, `update "user"`) {
					userTable = true
				}
			}
			for _, a := range c.Args {
				if mentionsType(a, "User") {
					userTable = true
				}
			}
		}
		if !userTable && !slices.ContainsFunc(lits, isAuditedColumn) {
			return false
		}
		for _, l := range lits {
			if isAuditedColumn(l) || slices.ContainsFunc(auditedUserColumns, func(c string) bool { return strings.Contains(l, c) && strings.Contains(strings.ToLower(l), "update") }) {
				return true
			}
		}
		return slices.ContainsFunc(call.Args, mapHasAuditedKey)
	case "Delete", "DeleteByBean", "DeleteBeans", "DeleteByID":
		return slices.ContainsFunc(append([]ast.Expr{call.Fun}, call.Args...), func(a ast.Expr) bool {
			return slices.ContainsFunc(auditedDeletes, func(typ string) bool { return mentionsType(a, typ) })
		})
	}
	return false
}

// auditedDeletes are the credential types whose deletes the audit log must see.
var auditedDeletes = []string{"PublicKey", "AccessToken", "GPGKey", "DeployKey", "OrgDeployKey", "OrgDeployKeyRepo"}

func isAuditedColumn(s string) bool {
	return slices.Contains(auditedUserColumns, s)
}

func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	case *ast.IndexExpr:
		return callName(f.X)
	case *ast.IndexListExpr:
		return callName(f.X)
	}
	return ""
}

// receiverCall is the call that a method call is made on, as in a.Table(...).Update(...).
func receiverCall(call *ast.CallExpr) *ast.CallExpr {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		if c, ok := sel.X.(*ast.CallExpr); ok {
			return c
		}
	}
	return nil
}

func literals(call *ast.CallExpr) []string {
	var out []string
	for _, a := range call.Args {
		if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
	}
	return out
}

// mentionsType reports whether e names the type (as T, pkg.T, &T{} or a type argument).
func mentionsType(e ast.Node, typ string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			found = found || n.Name == typ
		case *ast.FuncLit:
			return false
		}
		return !found
	})
	return found
}

func mapHasAuditedKey(e ast.Expr) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.BasicLit); ok {
				if s, err := strconv.Unquote(k.Value); err == nil && isAuditedColumn(s) {
					return true
				}
			}
		}
	}
	return false
}
