// Copyright 2014 The Gogs Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package admin

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"forgejo.org/models/auth"
	"forgejo.org/models/db"
	org_model "forgejo.org/models/organization"
	auth_module "forgejo.org/modules/auth"
	"forgejo.org/modules/auth/pam"
	"forgejo.org/modules/base"
	"forgejo.org/modules/log"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/web"
	auth_service "forgejo.org/services/auth"
	"forgejo.org/services/auth/source/ldap"
	"forgejo.org/services/auth/source/oauth2"
	pam_service "forgejo.org/services/auth/source/pam"
	"forgejo.org/services/auth/source/saml"
	"forgejo.org/services/auth/source/smtp"
	"forgejo.org/services/context"
	"forgejo.org/services/forms"

	"code.forgejo.org/xorm/xorm/convert"
)

const (
	tplAuths    base.TplName = "admin/auth/list"
	tplAuthNew  base.TplName = "admin/auth/new"
	tplAuthEdit base.TplName = "admin/auth/edit"
)

// Authentications show authentication config page
func Authentications(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.authentication")
	ctx.Data["PageIsAdminAuthentications"] = true

	var err error
	ctx.Data["Sources"], ctx.Data["Total"], err = db.FindAndCount[auth.Source](ctx, auth.FindSourcesOptions{})
	if err != nil {
		ctx.ServerError("auth.Sources", err)
		return
	}

	ctx.HTML(http.StatusOK, tplAuths)
}

type dropdownItem struct {
	Name string
	Type any
}

var (
	authSources = func() []dropdownItem {
		items := []dropdownItem{
			{auth.LDAP.String(), auth.LDAP},
			{auth.DLDAP.String(), auth.DLDAP},
			{auth.SMTP.String(), auth.SMTP},
			{auth.OAuth2.String(), auth.OAuth2},
			{auth.SAML.String(), auth.SAML},
		}
		if pam.Supported {
			items = append(items, dropdownItem{auth.Names[auth.PAM], auth.PAM})
		}
		return items
	}()

	securityProtocols = []dropdownItem{
		{ldap.SecurityProtocolNames[ldap.SecurityProtocolUnencrypted], ldap.SecurityProtocolUnencrypted},
		{ldap.SecurityProtocolNames[ldap.SecurityProtocolLDAPS], ldap.SecurityProtocolLDAPS},
		{ldap.SecurityProtocolNames[ldap.SecurityProtocolStartTLS], ldap.SecurityProtocolStartTLS},
	}
)

// NewAuthSource render adding a new auth source page
func NewAuthSource(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.auths.new")
	ctx.Data["PageIsAdminAuthentications"] = true

	ctx.Data["type"] = auth.LDAP.Int()
	ctx.Data["CurrentTypeName"] = auth.Names[auth.LDAP]
	ctx.Data["CurrentSecurityProtocol"] = ldap.SecurityProtocolNames[ldap.SecurityProtocolUnencrypted]
	ctx.Data["smtp_auth"] = "PLAIN"
	ctx.Data["is_active"] = true
	ctx.Data["is_sync_enabled"] = true
	ctx.Data["AuthSources"] = authSources
	ctx.Data["SecurityProtocols"] = securityProtocols
	ctx.Data["SMTPAuths"] = smtp.Authenticators
	oauth2providers := oauth2.GetSupportedOAuth2Providers()
	ctx.Data["OAuth2Providers"] = oauth2providers

	// only the first as default
	ctx.Data["oauth2_provider"] = oauth2providers[0].Name()
	ctx.Data["SAMLConfig"] = &saml.Source{NameIDFormat: saml.NameIDFormats[0]}
	ctx.Data["SAMLNameIDFormats"] = samlNameIDFormats(ctx)

	ctx.HTML(http.StatusOK, tplAuthNew)
}

func parseLDAPConfig(form forms.AuthenticationForm) *ldap.Source {
	var pageSize uint32
	if form.UsePagedSearch {
		pageSize = uint32(form.SearchPageSize)
	}
	return &ldap.Source{
		Name:                  form.Name,
		Host:                  form.Host,
		Port:                  form.Port,
		SecurityProtocol:      ldap.SecurityProtocol(form.SecurityProtocol),
		SkipVerify:            form.SkipVerify,
		BindDN:                form.BindDN,
		UserDN:                form.UserDN,
		BindPassword:          form.BindPassword,
		UserBase:              form.UserBase,
		DefaultDomainName:     form.DefaultDomainName,
		AttributeUsername:     form.AttributeUsername,
		AttributeName:         form.AttributeName,
		AttributeSurname:      form.AttributeSurname,
		AttributeMail:         form.AttributeMail,
		AttributesInBind:      form.AttributesInBind,
		AttributeSSHPublicKey: form.AttributeSSHPublicKey,
		AttributeAvatar:       form.AttributeAvatar,
		SearchPageSize:        pageSize,
		Filter:                form.Filter,
		GroupsEnabled:         form.GroupsEnabled,
		GroupDN:               form.GroupDN,
		GroupFilter:           form.GroupFilter,
		GroupMemberUID:        form.GroupMemberUID,
		GroupTeamMap:          form.GroupTeamMap,
		GroupTeamMapRemoval:   form.GroupTeamMapRemoval,
		UserUID:               form.UserUID,
		AdminFilter:           form.AdminFilter,
		RestrictedFilter:      form.RestrictedFilter,
		AllowDeactivateAll:    form.AllowDeactivateAll,
		Enabled:               true,
		SkipLocalTwoFA:        form.SkipLocalTwoFA,

		GroupUserGroupMap:        form.GroupUserGroupMap,
		GroupUserGroupMapRemoval: form.GroupUserGroupMapRemoval,
	}
}

func parseSMTPConfig(form forms.AuthenticationForm) *smtp.Source {
	return &smtp.Source{
		Auth:           form.SMTPAuth,
		Host:           form.SMTPHost,
		Port:           form.SMTPPort,
		AllowedDomains: form.AllowedDomains,
		ForceSMTPS:     form.ForceSMTPS,
		SkipVerify:     form.SkipVerify,
		HeloHostname:   form.HeloHostname,
		DisableHelo:    form.DisableHelo,
		SkipLocalTwoFA: form.SkipLocalTwoFA,
	}
}

func parseOAuth2Config(form forms.AuthenticationForm) *oauth2.Source {
	var customURLMapping *oauth2.CustomURLMapping
	if form.Oauth2UseCustomURL {
		customURLMapping = &oauth2.CustomURLMapping{
			TokenURL:   form.Oauth2TokenURL,
			AuthURL:    form.Oauth2AuthURL,
			ProfileURL: form.Oauth2ProfileURL,
			EmailURL:   form.Oauth2EmailURL,
			Tenant:     form.Oauth2Tenant,
		}
	} else {
		customURLMapping = nil
	}
	var scopes []string
	for s := range strings.SplitSeq(form.Oauth2Scopes, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			scopes = append(scopes, s)
		}
	}

	return &oauth2.Source{
		Provider:                      form.Oauth2Provider,
		ClientID:                      form.Oauth2Key,
		ClientSecret:                  form.Oauth2Secret,
		OpenIDConnectAutoDiscoveryURL: form.OpenIDConnectAutoDiscoveryURL,
		CustomURLMapping:              customURLMapping,
		IconURL:                       form.Oauth2IconURL,
		Scopes:                        scopes,
		AttributeSSHPublicKey:         form.Oauth2AttributeSSHPublicKey,
		RequiredClaimName:             form.Oauth2RequiredClaimName,
		RequiredClaimValue:            form.Oauth2RequiredClaimValue,
		SkipLocalTwoFA:                form.SkipLocalTwoFA,
		GroupClaimName:                form.Oauth2GroupClaimName,
		RestrictedGroup:               form.Oauth2RestrictedGroup,
		AdminGroup:                    form.Oauth2AdminGroup,
		GroupTeamMap:                  form.Oauth2GroupTeamMap,
		GroupTeamMapRemoval:           form.Oauth2GroupTeamMapRemoval,
		DynGroupMaps:                  form.Oauth2DynGroupMaps,
		DynGroupMapsRemoval:           form.Oauth2DynGroupMapsRemoval,
		AllowUsernameChange:           form.AllowUsernameChange,
		QuotaGroupClaimName:           form.Oauth2QuotaGroupClaimName,
		QuotaGroupMap:                 form.Oauth2QuotaGroupMap,
		QuotaGroupMapRemoval:          form.Oauth2QuotaGroupMapRemoval,

		GroupUserGroupMap:        form.Oauth2GroupUserGroupMap,
		GroupUserGroupMapRemoval: form.Oauth2GroupUserGroupMapRemoval,
	}
}

func parseSAMLConfig(form forms.AuthenticationForm) *saml.Source {
	return &saml.Source{
		IDPMetadataURL:             form.SAMLIDPMetadataURL,
		IDPMetadata:                form.SAMLIDPMetadata,
		IDPEntityID:                form.SAMLIDPEntityID,
		IDPSSOURL:                  form.SAMLIDPSSOURL,
		IDPSLOURL:                  form.SAMLIDPSLOURL,
		IDPCertificate:             form.SAMLIDPCertificate,
		SPEntityID:                 form.SAMLSPEntityID,
		SPCertificate:              form.SAMLSPCertificate,
		SPPrivateKey:               form.SAMLSPPrivateKey,
		SignRequests:               form.SAMLSignRequests,
		NameIDFormat:               form.SAMLNameIDFormat,
		UserIDAttribute:            form.SAMLUserIDAttribute,
		UsernameAttribute:          form.SAMLUsernameAttribute,
		FullNameAttribute:          form.SAMLFullNameAttribute,
		EmailAttribute:             form.SAMLEmailAttribute,
		RequireEncryptedAssertions: form.SAMLRequireEncryptedAssertions,
		ForceAuthn:                 form.SAMLForceAuthn,
		AllowIDPInitiated:          form.SAMLAllowIDPInitiated,
		IconURL:                    form.SAMLIconURL,
		SkipLocalTwoFA:             form.SAMLSkipLocalTwoFA,
		RequiredClaimName:          form.SAMLRequiredClaimName,
		RequiredClaimValue:         form.SAMLRequiredClaimValue,
		GroupClaimName:             form.SAMLGroupClaimName,
		AdminGroup:                 form.SAMLAdminGroup,
		RestrictedGroup:            form.SAMLRestrictedGroup,
		GroupTeamMap:               form.SAMLGroupTeamMap,
		GroupTeamMapRemoval:        form.SAMLGroupTeamMapRemoval,
		DynGroupMaps:               form.SAMLDynGroupMaps,
		DynGroupMapsRemoval:        form.SAMLDynGroupMapsRemoval,
		QuotaGroupClaimName:        form.SAMLQuotaGroupClaimName,
		QuotaGroupMap:              form.SAMLQuotaGroupMap,
		QuotaGroupMapRemoval:       form.SAMLQuotaGroupMapRemoval,
		GroupUserGroupMap:          form.SAMLGroupUserGroupMap,
		GroupUserGroupMapRemoval:   form.SAMLGroupUserGroupMapRemoval,
	}
}

// samlNameIDFormats lists the NameID formats with their names for the select.
func samlNameIDFormats(ctx *context.Context) []struct{ Value, Name string } {
	names := []string{
		ctx.Locale.TrString("admin.auths.saml_name_id_persistent"),
		ctx.Locale.TrString("admin.auths.saml_name_id_email"),
		ctx.Locale.TrString("admin.auths.saml_name_id_unspecified"),
		ctx.Locale.TrString("admin.auths.saml_name_id_transient"),
	}
	formats := make([]struct{ Value, Name string }, len(saml.NameIDFormats))
	for i, f := range saml.NameIDFormats {
		formats[i].Value, formats[i].Name = f, names[i]
	}
	return formats
}

// samlErrorText is the text of a refused SAML configuration, in the words of the form.
func samlErrorText(ctx *context.Context, ve *saml.ValidationError) template.HTML {
	detail := ""
	if ve.Detail != nil {
		detail = ve.Detail.Error()
	}
	switch ve.Reason {
	case "idp_missing":
		return ctx.Tr("admin.auths.saml_err_idp_missing")
	case "required":
		return ctx.Tr("admin.auths.saml_err_idp_required")
	case "not_http":
		return ctx.Tr("admin.auths.saml_err_not_http")
	case "metadata":
		switch ve.Field {
		case "idp_metadata_url":
			return ctx.Tr("admin.auths.saml_err_metadata_url", detail)
		case "idp_metadata":
			return ctx.Tr("admin.auths.saml_err_metadata", detail)
		}
		return ctx.Tr("admin.auths.saml_err_metadata_manual", detail)
	case "certificate":
		if ve.Field == "idp_certificate" {
			return ctx.Tr("admin.auths.saml_err_idp_certificate", detail)
		}
		return ctx.Tr("admin.auths.saml_err_sp_certificate", detail)
	case "private_key":
		return ctx.Tr("admin.auths.saml_err_sp_private_key", detail)
	case "pair_certificate":
		return ctx.Tr("admin.auths.saml_err_pair_certificate")
	case "pair_key":
		return ctx.Tr("admin.auths.saml_err_pair_key")
	case "mismatch":
		return ctx.Tr("admin.auths.saml_err_mismatch")
	case "needs_key":
		return ctx.Tr("admin.auths.saml_err_needs_key")
	case "transient":
		return ctx.Tr("admin.auths.saml_err_transient")
	case "entity_id":
		return ctx.Tr("admin.auths.saml_err_entity_id")
	}
	return ctx.Tr("admin.auths.saml_invalid", ve.Error())
}

// validateSAMLConfig renders an error and returns false when the SAML configuration can't be used. Only the field
// that was refused is marked (Err_SAML_<field>).
func validateSAMLConfig(ctx *context.Context, form forms.AuthenticationForm, config *saml.Source, tpl base.TplName) bool {
	if !slices.Contains(saml.NameIDFormats, config.NameIDFormat) {
		config.NameIDFormat = saml.NameIDFormats[0]
	}
	if err := config.Validate(ctx); err != nil {
		text := ctx.Tr("admin.auths.saml_invalid", err.Error())
		if ve, ok := errors.AsType[*saml.ValidationError](err); ok {
			ctx.Data["Err_SAML_"+ve.Field] = true
			text = samlErrorText(ctx, ve)
			ctx.Data["SAMLError"] = text // also shown under the field, far down the form
		}
		ctx.RenderWithErr(text, tpl, form)
		return false
	}
	return true
}

// checkGroupUserGroupMap renders an error and returns false if the directory
// group to user group mapping of the form names a user group that does not exist.
func checkGroupUserGroupMap(ctx *context.Context, form forms.AuthenticationForm, tpl base.TplName) bool {
	raw := form.GroupUserGroupMap
	switch auth.Type(form.Type) {
	case auth.OAuth2:
		raw = form.Oauth2GroupUserGroupMap
	case auth.SAML:
		raw = form.SAMLGroupUserGroupMap
	}
	mapping, err := auth_module.UnmarshalQuotaGroupMapping(raw)
	if err != nil {
		ctx.ServerError("UnmarshalQuotaGroupMapping", err) // the form binding already validated it
		return false
	}
	for _, names := range mapping {
		for name := range names {
			if _, err := org_model.GetUserGroupByName(ctx, name); org_model.IsErrUserGroupNotExist(err) {
				ctx.Data["Err_GroupUserGroupMap"] = true
				ctx.RenderWithErr(ctx.Tr("admin.auths.user_group_not_exist", name), tpl, form)
				return false
			} else if err != nil {
				ctx.ServerError("GetUserGroupByName", err)
				return false
			}
		}
	}
	return true
}

// NewAuthSourcePost response for adding an auth source
func NewAuthSourcePost(ctx *context.Context) {
	form := *web.GetForm(ctx).(*forms.AuthenticationForm)
	ctx.Data["Title"] = ctx.Tr("admin.auths.new")
	ctx.Data["PageIsAdminAuthentications"] = true

	ctx.Data["CurrentTypeName"] = auth.Type(form.Type).String()
	ctx.Data["CurrentSecurityProtocol"] = ldap.SecurityProtocolNames[ldap.SecurityProtocol(form.SecurityProtocol)]
	ctx.Data["AuthSources"] = authSources
	ctx.Data["SecurityProtocols"] = securityProtocols
	ctx.Data["SMTPAuths"] = smtp.Authenticators
	oauth2providers := oauth2.GetSupportedOAuth2Providers()
	ctx.Data["OAuth2Providers"] = oauth2providers
	ctx.Data["SAMLNameIDFormats"] = samlNameIDFormats(ctx)
	ctx.Data["SAMLConfig"] = &saml.Source{NameIDFormat: saml.NameIDFormats[0]}

	hasTLS := false
	var config convert.Conversion
	switch auth.Type(form.Type) {
	case auth.LDAP, auth.DLDAP:
		config = parseLDAPConfig(form)
		hasTLS = ldap.SecurityProtocol(form.SecurityProtocol) > ldap.SecurityProtocolUnencrypted
	case auth.SMTP:
		config = parseSMTPConfig(form)
		hasTLS = true
	case auth.PAM:
		config = &pam_service.Source{
			ServiceName:    form.PAMServiceName,
			EmailDomain:    form.PAMEmailDomain,
			SkipLocalTwoFA: form.SkipLocalTwoFA,
		}
	case auth.OAuth2:
		config = parseOAuth2Config(form)
		oauth2Config := config.(*oauth2.Source)
		if oauth2Config.Provider == "openidConnect" {
			discoveryURL, err := url.Parse(oauth2Config.OpenIDConnectAutoDiscoveryURL)
			if err != nil || (discoveryURL.Scheme != "http" && discoveryURL.Scheme != "https") {
				ctx.Data["Err_DiscoveryURL"] = true
				ctx.RenderWithErr(ctx.Tr("admin.auths.invalid_openIdConnectAutoDiscoveryURL"), tplAuthNew, form)
				return
			}
		}
	case auth.SAML:
		samlConfig := parseSAMLConfig(form)
		shown := *samlConfig
		shown.SPPrivateKey = "" // never rendered back into the form
		ctx.Data["SAMLKeyCleared"] = form.SAMLSPPrivateKey != ""
		ctx.Data["SAMLConfig"] = &shown
		config = samlConfig
		if !ctx.HasError() && !validateSAMLConfig(ctx, form, config.(*saml.Source), tplAuthNew) {
			return
		}
	default:
		ctx.Error(http.StatusBadRequest)
		return
	}
	ctx.Data["HasTLS"] = hasTLS

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplAuthNew)
		return
	}
	if !checkGroupUserGroupMap(ctx, form, tplAuthNew) {
		return
	}

	if err := auth.CreateSource(ctx, &auth.Source{
		Type:          auth.Type(form.Type),
		Name:          form.Name,
		IsActive:      form.IsActive,
		IsSyncEnabled: form.IsSyncEnabled,
		Cfg:           config,
	}); err != nil {
		if auth.IsErrSourceAlreadyExist(err) {
			ctx.Data["Err_Name"] = true
			ctx.RenderWithErr(ctx.Tr("admin.auths.login_source_exist", err.(auth.ErrSourceAlreadyExist).Name), tplAuthNew, form)
		} else if oauth2.IsErrOpenIDConnectInitialize(err) {
			ctx.Data["Err_DiscoveryURL"] = true
			unwrapped := err.(oauth2.ErrOpenIDConnectInitialize).Unwrap()
			ctx.RenderWithErr(ctx.Tr("admin.auths.unable_to_initialize_openid", unwrapped), tplAuthNew, form)
		} else {
			ctx.ServerError("auth.CreateSource", err)
		}
		return
	}

	log.Trace("Authentication created by admin(%s): %s", ctx.Doer.Name, form.Name)

	ctx.Flash.Success(ctx.Tr("admin.auths.new_success", form.Name))
	ctx.Redirect(setting.AppSubURL + "/admin/auths")
}

// EditAuthSource render editing auth source page
func EditAuthSource(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.auths.edit")
	ctx.Data["PageIsAdminAuthentications"] = true

	ctx.Data["SecurityProtocols"] = securityProtocols
	ctx.Data["SMTPAuths"] = smtp.Authenticators
	oauth2providers := oauth2.GetSupportedOAuth2Providers()
	ctx.Data["OAuth2Providers"] = oauth2providers

	source, err := auth.GetSourceByID(ctx, ctx.ParamsInt64(":authid"))
	if err != nil {
		ctx.ServerError("auth.GetSourceByID", err)
		return
	}
	ctx.Data["Source"] = source
	ctx.Data["HasTLS"] = source.HasTLS()

	if source.IsSAML() {
		setSAMLEditData(ctx, source)
	}
	if source.IsOAuth2() {
		type Named interface {
			Name() string
		}

		for _, provider := range oauth2providers {
			if provider.Name() == source.Cfg.(Named).Name() {
				ctx.Data["CurrentOAuth2Provider"] = provider
				break
			}
		}
	}

	ctx.HTML(http.StatusOK, tplAuthEdit)
}

// setSAMLEditData sets the template data of the SAML part of the edit page.
func setSAMLEditData(ctx *context.Context, source *auth.Source) {
	cfg := source.Cfg.(*saml.Source)
	ctx.Data["SAMLConfig"] = cfg
	ctx.Data["SAMLNameIDFormats"] = samlNameIDFormats(ctx)
	ctx.Data["SAMLHasPrivateKey"] = cfg.SPPrivateKey != ""
	ctx.Data["SAMLMetadataURL"] = saml.MetadataURL(source.Name)
	ctx.Data["SAMLACSURL"] = saml.ACSURL(source.Name)
	ctx.Data["SAMLSLOURL"] = saml.SLOURL(source.Name)
	ctx.Data["SAMLEntityID"] = cfg.EntityID()
}

// EditAuthSourcePost response for editing auth source
func EditAuthSourcePost(ctx *context.Context) {
	form := *web.GetForm(ctx).(*forms.AuthenticationForm)
	ctx.Data["Title"] = ctx.Tr("admin.auths.edit")
	ctx.Data["PageIsAdminAuthentications"] = true

	ctx.Data["SMTPAuths"] = smtp.Authenticators
	oauth2providers := oauth2.GetSupportedOAuth2Providers()
	ctx.Data["OAuth2Providers"] = oauth2providers

	source, err := auth.GetSourceByID(ctx, ctx.ParamsInt64(":authid"))
	if err != nil {
		ctx.ServerError("auth.GetSourceByID", err)
		return
	}
	ctx.Data["Source"] = source
	ctx.Data["HasTLS"] = source.HasTLS()
	if source.IsSAML() {
		setSAMLEditData(ctx, source)
	}

	if ctx.HasError() {
		ctx.HTML(http.StatusOK, tplAuthEdit)
		return
	}

	var config convert.Conversion
	switch auth.Type(form.Type) {
	case auth.LDAP, auth.DLDAP:
		config = parseLDAPConfig(form)
	case auth.SMTP:
		config = parseSMTPConfig(form)
	case auth.PAM:
		config = &pam_service.Source{
			ServiceName: form.PAMServiceName,
			EmailDomain: form.PAMEmailDomain,
		}
	case auth.OAuth2:
		config = parseOAuth2Config(form)
		oauth2Config := config.(*oauth2.Source)
		if oauth2Config.Provider == "openidConnect" {
			discoveryURL, err := url.Parse(oauth2Config.OpenIDConnectAutoDiscoveryURL)
			if err != nil || (discoveryURL.Scheme != "http" && discoveryURL.Scheme != "https") {
				ctx.Data["Err_DiscoveryURL"] = true
				ctx.RenderWithErr(ctx.Tr("admin.auths.invalid_openIdConnectAutoDiscoveryURL"), tplAuthEdit, form)
				return
			}
		}
	case auth.SAML:
		samlConfig := parseSAMLConfig(form)
		// an empty key field keeps the stored key while the certificate is set
		if old, ok := source.Cfg.(*saml.Source); ok && samlConfig.SPPrivateKey == "" && samlConfig.SPCertificate != "" {
			samlConfig.SPPrivateKey = old.SPPrivateKey
		}
		config = samlConfig
		shown := *samlConfig
		shown.SPPrivateKey = "" // never rendered back into the form
		ctx.Data["SAMLKeyCleared"] = form.SAMLSPPrivateKey != ""
		ctx.Data["SAMLConfig"] = &shown
		if !validateSAMLConfig(ctx, form, samlConfig, tplAuthEdit) {
			return
		}
	default:
		ctx.Error(http.StatusBadRequest)
		return
	}

	if !checkGroupUserGroupMap(ctx, form, tplAuthEdit) {
		return
	}

	source.Name = form.Name
	source.IsActive = form.IsActive
	source.IsSyncEnabled = form.IsSyncEnabled
	source.Cfg = config
	if err := auth.UpdateSource(ctx, source); err != nil {
		if auth.IsErrSourceAlreadyExist(err) {
			ctx.Data["Err_Name"] = true
			ctx.RenderWithErr(ctx.Tr("admin.auths.login_source_exist", err.(auth.ErrSourceAlreadyExist).Name), tplAuthEdit, form)
		} else if oauth2.IsErrOpenIDConnectInitialize(err) {
			ctx.Flash.Error(err.Error(), true)
			ctx.Data["Err_DiscoveryURL"] = true
			ctx.HTML(http.StatusOK, tplAuthEdit)
		} else {
			ctx.ServerError("UpdateSource", err)
		}
		return
	}
	log.Trace("Authentication changed by admin(%s): %d", ctx.Doer.Name, source.ID)

	ctx.Flash.Success(ctx.Tr("admin.auths.update_success"))
	ctx.Redirect(setting.AppSubURL + "/admin/auths/" + strconv.FormatInt(form.ID, 10))
}

// DeleteAuthSource response for deleting an auth source
func DeleteAuthSource(ctx *context.Context) {
	source, err := auth.GetSourceByID(ctx, ctx.ParamsInt64(":authid"))
	if err != nil {
		ctx.ServerError("auth.GetSourceByID", err)
		return
	}

	if err = auth_service.DeleteSource(ctx, source); err != nil {
		if auth.IsErrSourceInUse(err) {
			ctx.Flash.Error(ctx.Tr("admin.auths.still_in_used"))
		} else {
			ctx.Flash.Error(fmt.Sprintf("auth_service.DeleteSource: %v", err))
		}
		ctx.JSONRedirect(setting.AppSubURL + "/admin/auths/" + url.PathEscape(ctx.Params(":authid")))
		return
	}
	log.Trace("Authentication deleted by admin(%s): %d", ctx.Doer.Name, source.ID)

	ctx.Flash.Success(ctx.Tr("admin.auths.deletion_success"))
	ctx.JSONRedirect(setting.AppSubURL + "/admin/auths")
}
