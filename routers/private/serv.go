// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	asymkey_model "forgejo.org/models/asymkey"
	audit_model "forgejo.org/models/audit"
	"forgejo.org/models/auth"
	"forgejo.org/models/perm"
	access_model "forgejo.org/models/perm/access"
	quota_model "forgejo.org/models/quota"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unit"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/log"
	"forgejo.org/modules/private"
	"forgejo.org/modules/ratelimit"
	repo_module "forgejo.org/modules/repository"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/util"
	"forgejo.org/routers/common"
	auth_service "forgejo.org/services/auth"
	"forgejo.org/services/context"
	repo_service "forgejo.org/services/repository"
	wiki_service "forgejo.org/services/wiki"
)

var sshLogger = log.GetManager().GetLogger("ssh")

func checkTwoFactor(ctx *context.PrivateContext, user *user_model.User) {
	if !user.MustHaveTwoFactor() {
		return
	}

	hasTwoFactor, err := auth.HasTwoFactorByUID(ctx, user.ID)
	if err != nil {
		sshLogger.Error("Error getting 2fa: %s", err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Error getting 2fa: %s", err),
		})
		return
	}
	if !hasTwoFactor {
		ctx.JSON(http.StatusForbidden, private.Response{
			UserMsg: ctx.Locale.TrString("error.must_enable_2fa", fmt.Sprintf("%suser/settings/security", setting.AppURL)),
		})
		return
	}
}

// sshActor makes the request's audit actor (the "private" one of this request) the SSH client: the key it
// authenticated with and the client IP that cmd/serv read from SSH_CONNECTION.
func sshActor(ctx *context.PrivateContext, keyID int64) *audit_model.Actor {
	a := audit_model.ActorFromContext(ctx)
	ip := ctx.FormString("ip")
	if addr, err := netip.ParseAddr(ip); err == nil {
		ip = addr.WithZone("").String()
	}
	*a = audit_model.Actor{
		Type: audit_model.ActorAnonymous, IP: ip, RequestID: util.CryptoRandomString(util.RandomStringMedium),
		Credential: audit_model.Credential{Type: audit_model.CredentialSSHKey, ID: keyID},
	}
	return a
}

// deployKeyCredential is the audit credential of an effective deploy key on repo.
func deployKeyCredential(key *asymkey_model.EffectiveDeployKey, repo *repo_model.Repository) audit_model.Credential {
	c := audit_model.Credential{Type: audit_model.CredentialDeployKey, Name: key.Name, OwnerID: repo.OwnerID}
	switch {
	case key.RepoKey != nil:
		c.ID = key.RepoKey.ID
	case key.OrgKey.GroupID != 0:
		c.Type, c.ID = audit_model.CredentialProjectDeployKey, key.OrgKey.ID
	default:
		c.Type, c.ID = audit_model.CredentialOrgDeployKey, key.OrgKey.ID
	}
	return c
}

func expiredKeyMsg(key *asymkey_model.PublicKey) string {
	return fmt.Sprintf("SSH key %q expired on %s. Add a new key in your settings.", key.Name, key.ExpiresUnix.FormatDate())
}

// ServNoCommand returns information about the provided keyid
func ServNoCommand(ctx *context.PrivateContext) {
	keyID := ctx.ParamsInt64(":keyid")
	if keyID <= 0 {
		ctx.JSON(http.StatusBadRequest, private.Response{
			UserMsg: fmt.Sprintf("Bad key id: %d", keyID),
		})
		return
	}
	results := private.KeyAndOwner{}

	key, err := asymkey_model.GetPublicKeyByID(ctx, keyID)
	if err != nil {
		if asymkey_model.IsErrKeyNotExist(err) {
			ctx.JSON(http.StatusUnauthorized, private.Response{
				UserMsg: fmt.Sprintf("Cannot find key: %d", keyID),
			})
			return
		}
		sshLogger.Error("Unable to get public key: %d Error: %v", keyID, err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: err.Error(),
		})
		return
	}
	results.Key = key

	if key.IsExpired() {
		sshActor(ctx, keyID)
		auth_service.RecordExpired(ctx, audit_model.CredentialSSHKey, key.ID, key.OwnerID, key.Name, key.ExpiresUnix)
		ctx.JSON(http.StatusUnauthorized, private.Response{
			UserMsg: expiredKeyMsg(key),
		})
		return
	}

	if key.Type == asymkey_model.KeyTypeUser || key.Type == asymkey_model.KeyTypePrincipal {
		user, err := user_model.GetUserByID(ctx, key.OwnerID)
		if err != nil {
			if user_model.IsErrUserNotExist(err) {
				ctx.JSON(http.StatusUnauthorized, private.Response{
					UserMsg: fmt.Sprintf("Cannot find owner with id: %d for key: %d", key.OwnerID, keyID),
				})
				return
			}
			sshLogger.Error("Unable to get owner with id: %d for public key: %d Error: %v", key.OwnerID, keyID, err)
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: err.Error(),
			})
			return
		}
		if !user.IsActive || user.ProhibitLogin {
			ctx.JSON(http.StatusForbidden, private.Response{
				UserMsg: "Your account is disabled.",
			})
			return
		}

		checkTwoFactor(ctx, user)
		if ctx.Written() {
			return
		}

		results.Owner = user
	}
	ctx.JSON(http.StatusOK, &results)
}

// sshRateLimited answers 429 and returns true when the key or the client IP is out of [rate_limit.ssh] tokens. serv is
// reached only through an SSH connection that authenticated this key, and the internal API needs the internal
// token, so nobody can spend the budget of a key they don't hold. A missing or unparsable client IP skips the IP
// bucket (the IP allowlist refuses it when SSH is enforced). Admins' keys are exempt with EXEMPT_ADMINS, deploy keys
// never.
func sshRateLimited(ctx *context.PrivateContext, keyID int64) bool {
	if !setting.RateLimit.Enabled {
		return false
	}
	addr := ""
	if ip, err := netip.ParseAddr(ctx.FormString("ip")); err == nil {
		addr = net.JoinHostPort(ip.WithZone("").String(), "0") // a zone would make IPKey read it as invalid
	}
	r := ratelimit.CheckSSH(addr, keyID)
	if r.Allowed {
		return false
	}
	// the admin lookup only happens for a request that would be refused
	if setting.RateLimit.ExemptAdmins {
		if key, err := asymkey_model.GetPublicKeyByID(ctx, keyID); err == nil && key.Type != asymkey_model.KeyTypeDeploy {
			if u, err := user_model.GetUserByID(ctx, key.OwnerID); err == nil && u.IsAdmin && u.IsActive && !u.ProhibitLogin {
				return false
			}
		}
	}
	sshLogger.Warn("SSH rate limit exceeded by key %d from %q", keyID, ctx.FormString("ip"))
	auth_service.RecordRefusal(ctx, "rate_limit", 0, 0, ratelimit.SSH+"|"+auth_service.ActorIPKey(ctx, 48)+"|key:"+strconv.FormatInt(keyID, 10), map[string]any{"class": ratelimit.SSH})
	ctx.JSON(http.StatusTooManyRequests, private.Response{UserMsg: (&ratelimit.LimitedError{Result: r}).Error()})
	return true
}

// ServCommand returns information about the provided keyid
func ServCommand(ctx *context.PrivateContext) {
	keyID := ctx.ParamsInt64(":keyid")
	actor := sshActor(ctx, keyID)
	if sshRateLimited(ctx, keyID) {
		return
	}
	ownerName := ctx.Params(":owner")
	repoName := ctx.Params(":repo")
	mode := perm.AccessMode(ctx.FormInt("mode"))

	// Set the basic parts of the results to return
	results := private.ServCommandResults{
		RepoName:  repoName,
		OwnerName: ownerName,
		KeyID:     keyID,
	}

	// a push lowers mode to read for AGit below; the size limit refuses every write
	isWrite := mode > perm.AccessModeRead

	// Now because we're not translating things properly let's just default some English strings here
	modeString := "read"
	if mode > perm.AccessModeRead {
		modeString = "write to"
	}

	maintenance := setting.GetMaintenance(ctx)
	if mode > perm.AccessModeRead && maintenance.ReadOnly() {
		ctx.JSON(http.StatusForbidden, private.Response{UserMsg: maintenance.Refusal()})
		return
	}

	// The default unit we're trying to look at is code
	unitType := unit.TypeCode

	// Unless we're a wiki...
	if strings.HasSuffix(repoName, ".wiki") {
		// in which case we need to look at the wiki
		unitType = unit.TypeWiki
		// And we'd better munge the reponame and tell downstream we're looking at a wiki
		results.IsWiki = true
		results.RepoName = repoName[:len(repoName)-5]
	}

	// a repository in a project: owner/project/repo
	groupName := ctx.Params(":group")
	fullName := results.OwnerName + "/" + results.RepoName
	if groupName != "" {
		fullName = results.OwnerName + "/" + groupName + "/" + results.RepoName
	}

	owner, err := user_model.GetUserByName(ctx, results.OwnerName)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			// User is fetching/cloning a non-existent repository
			sshLogger.Warn("Failed authentication attempt (cannot find repository: %s) from %s", fullName, ctx.RemoteAddr())
			ctx.JSON(http.StatusNotFound, private.Response{
				UserMsg: "Cannot find repository: " + fullName,
			})
			return
		}
		sshLogger.Error("Unable to get repository owner: %s/%s Error: %v", results.OwnerName, results.RepoName, err)
		ctx.JSON(http.StatusForbidden, private.Response{
			UserMsg: fmt.Sprintf("Unable to get repository owner: %s/%s %v", results.OwnerName, results.RepoName, err),
		})
		return
	}
	if !owner.IsOrganization() && !owner.IsActive {
		ctx.JSON(http.StatusForbidden, private.Response{
			UserMsg: "Repository cannot be accessed, you could retry it later",
		})
		return
	}

	// Now get the Repository and set the results section
	repoExist := true
	var repo *repo_model.Repository
	if groupName != "" {
		repo, err = visibleGroupRepository(ctx, keyID, owner, groupName, results.RepoName)
	} else {
		repo, err = repo_model.GetRepositoryByName(ctx, owner.ID, results.RepoName)
	}
	if err != nil {
		if repo_model.IsErrRepoNotExist(err) {
			repoExist = false
			// a repository in a project is never created by a push, and a hidden one is missing
			if groupName != "" || slices.Contains(ctx.FormStrings("verb"), "git-upload-pack") {
				// User is fetching/cloning a non-existent repository
				sshLogger.Warn("Failed authentication attempt (cannot find repository: %s) from %s", fullName, ctx.RemoteAddr())
				ctx.JSON(http.StatusNotFound, private.Response{
					UserMsg: "Cannot find repository: " + fullName,
				})
				return
			}
		} else {
			sshLogger.Error("Unable to get repository: %s/%s Error: %v", results.OwnerName, results.RepoName, err)
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: fmt.Sprintf("Unable to get repository: %s/%s %v", results.OwnerName, results.RepoName, err),
			})
			return
		}
	}

	if repoExist {
		repo.Owner = owner
		repo.OwnerName = ownerName
		results.RepoID = repo.ID
		if groupName != "" {
			// cmd/serv runs git on the path of the repository found here, never on the one the client sent
			repo.OwnerName = owner.Name
			results.OwnerName, results.GroupName, results.RepoName = owner.Name, repo.GroupName, repo.Name
		}

		if repo.IsBeingCreated() {
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: "Repository is being created, you could retry after it finished",
			})
			return
		}

		if repo.IsBroken() {
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: "Repository is in a broken state",
			})
			return
		}

		// We can shortcut at this point if the repo is a mirror
		if mode > perm.AccessModeRead && repo.IsMirror {
			ctx.JSON(http.StatusForbidden, private.Response{
				UserMsg: fmt.Sprintf("Mirror Repository %s/%s is read-only", results.OwnerName, results.RepoName),
			})
			return
		}
	}

	// Get the Public Key represented by the keyID
	key, err := asymkey_model.GetPublicKeyByID(ctx, keyID)
	if err != nil {
		if asymkey_model.IsErrKeyNotExist(err) {
			ctx.JSON(http.StatusNotFound, private.Response{
				UserMsg: fmt.Sprintf("Cannot find key: %d", keyID),
			})
			return
		}
		sshLogger.Error("Unable to get public key: %d Error: %v", keyID, err)
		ctx.JSON(http.StatusInternalServerError, private.Response{
			Err: fmt.Sprintf("Unable to get key: %d  Error: %v", keyID, err),
		})
		return
	}
	results.KeyName = key.Name
	results.KeyID = key.ID
	results.UserID = key.OwnerID
	results.ExpiresUnix = key.ExpiresUnix

	if key.IsExpired() {
		auth_service.RecordExpired(ctx, audit_model.CredentialSSHKey, key.ID, key.OwnerID, key.Name, key.ExpiresUnix)
		ctx.JSON(http.StatusUnauthorized, private.Response{
			UserMsg: expiredKeyMsg(key),
		})
		return
	}

	// If repo doesn't exist, deploy key doesn't make sense
	if !repoExist && key.Type == asymkey_model.KeyTypeDeploy {
		ctx.JSON(http.StatusNotFound, private.Response{
			UserMsg: fmt.Sprintf("Cannot find repository %s/%s", results.OwnerName, results.RepoName),
		})
		return
	}

	// Deploy Keys have ownerID set to 0 therefore we can't use the owner
	// So now we need to check if the key is a deploy key
	// We'll keep hold of the deploy key here for permissions checking
	var deployKey *asymkey_model.EffectiveDeployKey
	var user *user_model.User
	if key.Type == asymkey_model.KeyTypeDeploy {
		var err error
		deployKey, err = asymkey_model.GetEffectiveDeployKey(ctx, key.ID, repo)
		if err != nil {
			if asymkey_model.IsErrDeployKeyNotExist(err) {
				ctx.JSON(http.StatusNotFound, private.Response{
					UserMsg: fmt.Sprintf("Public (Deploy) Key: %d:%s is not authorized to %s %s/%s.", key.ID, key.Name, modeString, results.OwnerName, results.RepoName),
				})
				return
			}
			sshLogger.Error("Unable to get deploy for public (deploy) key: %d in %-v Error: %v", key.ID, repo, err)
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: fmt.Sprintf("Unable to get Deploy Key for Public Key: %d:%s in %s/%s.", key.ID, key.Name, results.OwnerName, results.RepoName),
			})
			return
		}
		actor.Type, actor.Credential = audit_model.ActorDeployKey, deployKeyCredential(deployKey, repo)
		if deployKey.IsExpired() {
			c := actor.Credential
			auth_service.RecordExpired(ctx, c.Type, c.ID, c.OwnerID, c.Name, deployKey.ExpiresUnix)
			ctx.JSON(http.StatusUnauthorized, private.Response{
				UserMsg: fmt.Sprintf("Deploy key %q expired on %s.", deployKey.Name, deployKey.ExpiresUnix.FormatDate()),
			})
			return
		}
		// the hook decides again from the public key and the repository, never from the pusher ID
		results.DeployKeyKeyID = key.ID
		results.KeyName = deployKey.Name
		results.ExpiresUnix = deployKey.ExpiresUnix

		// FIXME: Deploy keys aren't really the owner of the repo pushing changes
		// however we don't have good way of representing deploy keys in hook.go
		// so for now use the owner of the repository
		results.UserName = results.OwnerName
		results.UserID = repo.OwnerID
		if !repo.Owner.KeepEmailPrivate {
			results.UserEmail = repo.Owner.Email
		}
	} else {
		// Get the user represented by the Key
		var err error
		user, err = user_model.GetUserByID(ctx, key.OwnerID)
		if err != nil {
			if user_model.IsErrUserNotExist(err) {
				ctx.JSON(http.StatusUnauthorized, private.Response{
					UserMsg: fmt.Sprintf("Public Key: %d:%s owner %d does not exist.", key.ID, key.Name, key.OwnerID),
				})
				return
			}
			sshLogger.Error("Unable to get owner: %d for public key: %d:%s Error: %v", key.OwnerID, key.ID, key.Name, err)
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: fmt.Sprintf("Unable to get Owner: %d for Deploy Key: %d:%s in %s/%s.", key.OwnerID, key.ID, key.Name, ownerName, repoName),
			})
			return
		}

		actor.Type, actor.ID, actor.Name = audit_model.ActorUser, user.ID, user.Name
		actor.Credential = audit_model.Credential{Type: audit_model.CredentialSSHKey, ID: key.ID, Name: key.Name, OwnerID: user.ID}
		if !user.IsActive || user.ProhibitLogin {
			ctx.JSON(http.StatusForbidden, private.Response{
				UserMsg: "Your account is disabled.",
			})
			return
		}

		checkTwoFactor(ctx, user)
		if ctx.Written() {
			return
		}

		results.UserName = user.Name
		if !user.KeepEmailPrivate {
			results.UserEmail = user.Email
		}
	}

	// the IP allowlist: cmd/serv sends the client address from SSH_CONNECTION, refused if missing or unparsable
	if ip, refused := common.IPAllowlistRefused(ctx, setting.IPFromRemoteAddr(net.JoinHostPort(ctx.FormString("ip"), "0")), setting.IPAllowlistSurfaceSSH, user, nil); refused {
		ctx.JSON(http.StatusForbidden, private.Response{UserMsg: common.IPAllowlistRefusal(ip)})
		return
	}

	// in full maintenance mode only site admins can use git, deploy keys included
	if maintenance.Full() && !common.IsMaintenanceAdmin(user) {
		ctx.JSON(http.StatusForbidden, private.Response{UserMsg: maintenance.Refusal()})
		return
	}
	audit_model.RecordCredentialUsed(ctx, "ssh")

	// Don't allow pushing if the repo is archived
	if repoExist && mode > perm.AccessModeRead && repo.IsArchived {
		ctx.JSON(http.StatusUnauthorized, private.Response{
			UserMsg: fmt.Sprintf("Repo: %s/%s is archived.", results.OwnerName, results.RepoName),
		})
		return
	}

	// Permissions checking:
	if repoExist &&
		(mode > perm.AccessModeRead ||
			repo.IsPrivate ||
			owner.Visibility.IsPrivate() ||
			(user != nil && user.IsRestricted) || // user will be nil if the key is a deploykey
			setting.Service.RequireSignInView) {
		if key.Type == asymkey_model.KeyTypeDeploy {
			if deployKey.Mode < mode {
				ctx.JSON(http.StatusUnauthorized, private.Response{
					UserMsg: fmt.Sprintf("Deploy Key: %d:%s is not authorized to %s %s/%s.", key.ID, key.Name, modeString, results.OwnerName, results.RepoName),
				})
				return
			}
		} else {
			// The auditor role only applies to reads: decide before the mode is lowered for pushes.
			isRead := mode == perm.AccessModeRead

			// We don't know yet which references the Git client wants to write to,
			// but for AGit flow we have to degrade this check to a read permission.
			// So if we support proc-receive (needed for AGit flow) and the unit type
			// is code and we know the Git client wants to write to us, then degrade
			// the permission check to read. The pre-receive hook will do another
			// permission check which ensure for non AGit flow references the write
			// permission is checked.
			if unitType == unit.TypeCode && ctx.FormString("verb") == "git-receive-pack" {
				mode = perm.AccessModeRead
			}

			var perm access_model.Permission
			if isRead {
				perm, err = access_model.GetUserRepoReadPermission(ctx, repo, user, nil)
			} else {
				perm, err = access_model.GetUserRepoPermission(ctx, repo, user)
			}
			if err != nil {
				sshLogger.Error("Unable to get permissions for %-v with key %d in %-v Error: %v", user, key.ID, repo, err)
				ctx.JSON(http.StatusInternalServerError, private.Response{
					Err: fmt.Sprintf("Unable to get permissions for user %d:%s with key %d in %s/%s Error: %v", user.ID, user.Name, key.ID, results.OwnerName, results.RepoName, err),
				})
				return
			}

			userMode := perm.UnitAccessMode(unitType)

			if userMode < mode {
				sshLogger.Warn("Failed authentication attempt for %s with key %s (not authorized to %s %s/%s) from %s", user.Name, key.Name, modeString, ownerName, repoName, ctx.RemoteAddr())
				ctx.JSON(http.StatusUnauthorized, private.Response{
					UserMsg: fmt.Sprintf("User: %d:%s with Key: %d:%s is not authorized to %s %s/%s.", user.ID, user.Name, key.ID, key.Name, modeString, ownerName, repoName),
				})
				return
			}
		}
	}

	// The size limit of the owner's personal repositories refuses every push (one that only deletes too) and LFS
	// upload, after the permission checks; push-to-create only in the pusher's own namespace (PushCreateRepo
	// refuses the others).
	if isWrite && (repoExist || user != nil && user.ID == owner.ID) {
		if l, err := quota_model.GetPersonalRepoLimit(ctx, owner.ID); err != nil {
			ctx.JSON(http.StatusInternalServerError, private.Response{Err: fmt.Sprintf("GetPersonalRepoLimit: %v", err)})
			return
		} else if l.Reached() {
			var doerID int64
			if user != nil {
				doerID = user.ID
			}
			sshLogger.Warn("Personal repository size limit reached for %s (%d of %d bytes): SSH write to %s refused", owner.Name, l.Used, l.Limit, fullName)
			ctx.JSON(http.StatusForbidden, private.Response{UserMsg: l.MessageFor(doerID, owner.ID)})
			return
		}
	}

	// We already know we aren't using a deploy key
	if !repoExist {
		owner, err := user_model.GetUserByName(ctx, ownerName)
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: fmt.Sprintf("Unable to get owner: %s %v", results.OwnerName, err),
			})
			return
		}

		if owner.IsOrganization() && !setting.Repository.EnablePushCreateOrg {
			ctx.JSON(http.StatusForbidden, private.Response{
				UserMsg: "Push to create is not enabled for organizations.",
			})
			return
		}
		if !owner.IsOrganization() && !setting.Repository.EnablePushCreateUser {
			ctx.JSON(http.StatusForbidden, private.Response{
				UserMsg: "Push to create is not enabled for users.",
			})
			return
		}

		repo, err = repo_service.PushCreateRepo(ctx, user, owner, results.RepoName)
		if errors.As(err, &repo_module.ErrPersonalRepoPolicy{}) || errors.As(err, &quota_model.ErrPersonalRepoLimit{}) {
			ctx.JSON(http.StatusForbidden, private.Response{UserMsg: err.Error()})
			return
		}
		if err != nil {
			sshLogger.Error("pushCreateRepo: %v", err)
			ctx.JSON(http.StatusNotFound, private.Response{
				UserMsg: fmt.Sprintf("Cannot find repository: %s/%s", results.OwnerName, results.RepoName),
			})
			return
		}
		results.RepoID = repo.ID
	}

	if results.IsWiki {
		// Ensure the wiki is enabled before we allow access to it
		if _, err := repo.GetUnit(ctx, unit.TypeWiki); err != nil {
			if repo_model.IsErrUnitTypeNotExist(err) {
				ctx.JSON(http.StatusForbidden, private.Response{
					UserMsg: "repository wiki is disabled",
				})
				return
			}
			sshLogger.Error("Failed to get the wiki unit in %-v Error: %v", repo, err)
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: fmt.Sprintf("Failed to get the wiki unit in %s/%s Error: %v", ownerName, repoName, err),
			})
			return
		}

		// Finally if we're trying to touch the wiki we should init it
		if err = wiki_service.InitWiki(ctx, repo); err != nil {
			sshLogger.Error("Failed to initialize the wiki in %-v Error: %v", repo, err)
			ctx.JSON(http.StatusInternalServerError, private.Response{
				Err: fmt.Sprintf("Failed to initialize the wiki in %s/%s Error: %v", ownerName, repoName, err),
			})
			return
		}
	}
	deployKeySource := ""
	if deployKey != nil && deployKey.RepoKey != nil {
		deployKeySource = fmt.Sprintf("repo %d", deployKey.RepoKey.ID)
	} else if deployKey != nil {
		deployKeySource = fmt.Sprintf("org %d", deployKey.OrgKey.ID)
	}
	sshLogger.Info("Serv Results:\n\tIsWiki: %t\n\tDeployKeyKeyID: %d\tDeployKey: %s\n\tKeyID: %d\tKeyName: %s\n\tUserName: %s\n\tUserID: %d\n\tOwnerName: %s\n\tRepoName: %s\n\tRepoID: %d",
		results.IsWiki,
		results.DeployKeyKeyID,
		deployKeySource,
		results.KeyID,
		results.KeyName,
		results.UserName,
		results.UserID,
		results.OwnerName,
		results.RepoName,
		results.RepoID)

	ctx.JSON(http.StatusOK, results)
	// We will update the keys in a different call.
}

// visibleGroupRepository returns the repository in a project if the key may see it: a deploy key that covers it,
// or a user key whose owner can read it. A hidden repository is not found, before any message about it.
func visibleGroupRepository(ctx *context.PrivateContext, keyID int64, owner *user_model.User, groupName, repoName string) (*repo_model.Repository, error) {
	notExist := repo_model.ErrRepoNotExist{OwnerName: owner.Name, Name: repoName}
	repo, err := repo_model.GetRepositoryByPath(ctx, owner.ID, groupName, repoName)
	if err != nil {
		return nil, err
	}
	repo.Owner = owner
	key, err := asymkey_model.GetPublicKeyByID(ctx, keyID)
	if asymkey_model.IsErrKeyNotExist(err) {
		return nil, notExist
	} else if err != nil {
		return nil, err
	}
	if key.Type == asymkey_model.KeyTypeDeploy {
		if _, err := asymkey_model.GetEffectiveDeployKey(ctx, key.ID, repo); asymkey_model.IsErrDeployKeyNotExist(err) {
			return nil, notExist
		} else if err != nil {
			return nil, err
		}
		return repo, nil
	}
	user, err := user_model.GetUserByID(ctx, key.OwnerID)
	if user_model.IsErrUserNotExist(err) {
		return nil, notExist
	} else if err != nil {
		return nil, err
	}
	perm, err := access_model.GetUserRepoReadPermission(ctx, repo, user, nil)
	if err != nil {
		return nil, err
	}
	if !perm.HasAccess() {
		return nil, notExist
	}
	return repo, nil
}
