// Copyright 2014 The Gogs Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package forms

import (
	"net/http"

	"forgejo.org/modules/web/middleware"
	"forgejo.org/services/context"

	"code.forgejo.org/go-chi/binding"
)

// AuthenticationForm form for authentication
type AuthenticationForm struct {
	ID                            int64
	Type                          int    `binding:"Range(2,9)"`
	Name                          string `binding:"Required;MaxSize(30)"`
	Host                          string
	Port                          int
	BindDN                        string
	BindPassword                  string
	UserBase                      string
	UserDN                        string
	AttributeUsername             string
	AttributeName                 string
	AttributeSurname              string
	DefaultDomainName             string
	AttributeMail                 string
	AttributeSSHPublicKey         string
	AttributeAvatar               string
	AttributesInBind              bool
	UsePagedSearch                bool
	SearchPageSize                int
	Filter                        string
	AdminFilter                   string
	GroupsEnabled                 bool
	GroupDN                       string
	GroupFilter                   string
	GroupMemberUID                string
	UserUID                       string
	RestrictedFilter              string
	AllowDeactivateAll            bool
	IsActive                      bool
	IsSyncEnabled                 bool
	SMTPAuth                      string
	SMTPHost                      string
	SMTPPort                      int
	AllowedDomains                string
	SecurityProtocol              int `binding:"Range(0,2)"`
	TLS                           bool
	SkipVerify                    bool
	HeloHostname                  string
	DisableHelo                   bool
	ForceSMTPS                    bool
	PAMServiceName                string
	PAMEmailDomain                string
	Oauth2Provider                string
	Oauth2Key                     string `preprocess:"TrimSpace"`
	Oauth2Secret                  string `preprocess:"TrimSpace"`
	OpenIDConnectAutoDiscoveryURL string
	Oauth2UseCustomURL            bool
	Oauth2TokenURL                string
	Oauth2AuthURL                 string
	Oauth2ProfileURL              string
	Oauth2EmailURL                string
	Oauth2IconURL                 string
	Oauth2Tenant                  string
	Oauth2Scopes                  string
	Oauth2RequiredClaimName       string
	Oauth2RequiredClaimValue      string
	Oauth2GroupClaimName          string
	Oauth2AdminGroup              string
	Oauth2RestrictedGroup         string
	Oauth2GroupTeamMap            string `binding:"ValidGroupTeamMap"`
	Oauth2GroupTeamMapRemoval     bool
	Oauth2DynGroupMaps            string `binding:"ValidDynGroupMaps"`
	Oauth2DynGroupMapsRemoval     bool
	Oauth2QuotaGroupClaimName     string
	Oauth2QuotaGroupMap           string `binding:"ValidQuotaGroupMap"`
	Oauth2QuotaGroupMapRemoval    bool
	Oauth2AttributeSSHPublicKey   string
	SkipLocalTwoFA                bool
	GroupTeamMap                  string `binding:"ValidGroupTeamMap"`
	GroupTeamMapRemoval           bool
	AllowUsernameChange           bool

	GroupUserGroupMap              string `binding:"ValidQuotaGroupMap"`
	GroupUserGroupMapRemoval       bool
	Oauth2GroupUserGroupMap        string `binding:"ValidQuotaGroupMap"`
	Oauth2GroupUserGroupMapRemoval bool

	SAMLIDPMetadataURL             string `form:"saml_idp_metadata_url" preprocess:"TrimSpace"`
	SAMLIDPMetadata                string `form:"saml_idp_metadata" preprocess:"TrimSpace"`
	SAMLIDPEntityID                string `form:"saml_idp_entity_id" preprocess:"TrimSpace"`
	SAMLIDPSSOURL                  string `form:"saml_idp_sso_url" preprocess:"TrimSpace"`
	SAMLIDPSLOURL                  string `form:"saml_idp_slo_url" preprocess:"TrimSpace"`
	SAMLIDPCertificate             string `form:"saml_idp_certificate" preprocess:"TrimSpace"`
	SAMLSPEntityID                 string `form:"saml_sp_entity_id" preprocess:"TrimSpace"`
	SAMLSPCertificate              string `form:"saml_sp_certificate" preprocess:"TrimSpace"`
	SAMLSPPrivateKey               string `form:"saml_sp_private_key" preprocess:"TrimSpace"`
	SAMLSignRequests               bool   `form:"saml_sign_requests"`
	SAMLRequireEncryptedAssertions bool   `form:"saml_require_encrypted_assertions"`
	SAMLNameIDFormat               string `form:"saml_name_id_format"`
	SAMLUserIDAttribute            string `form:"saml_user_id_attribute" preprocess:"TrimSpace"`
	SAMLUsernameAttribute          string `form:"saml_username_attribute" preprocess:"TrimSpace"`
	SAMLFullNameAttribute          string `form:"saml_full_name_attribute" preprocess:"TrimSpace"`
	SAMLEmailAttribute             string `form:"saml_email_attribute" preprocess:"TrimSpace"`
	SAMLForceAuthn                 bool   `form:"saml_force_authn"`
	SAMLAllowIDPInitiated          bool   `form:"saml_allow_idp_initiated"`
	SAMLIconURL                    string `form:"saml_icon_url" preprocess:"TrimSpace"`
	SAMLRequiredClaimName          string `form:"saml_required_claim_name"`
	SAMLRequiredClaimValue         string `form:"saml_required_claim_value"`
	SAMLGroupClaimName             string `form:"saml_group_claim_name"`
	SAMLAdminGroup                 string `form:"saml_admin_group"`
	SAMLRestrictedGroup            string `form:"saml_restricted_group"`
	SAMLGroupTeamMap               string `form:"saml_group_team_map" binding:"ValidGroupTeamMap"`
	SAMLGroupTeamMapRemoval        bool   `form:"saml_group_team_map_removal"`
	SAMLDynGroupMaps               string `form:"saml_dyn_group_maps" binding:"ValidDynGroupMaps"`
	SAMLDynGroupMapsRemoval        bool   `form:"saml_dyn_group_maps_removal"`
	SAMLQuotaGroupClaimName        string `form:"saml_quota_group_claim_name"`
	SAMLQuotaGroupMap              string `form:"saml_quota_group_map" binding:"ValidQuotaGroupMap"`
	SAMLQuotaGroupMapRemoval       bool   `form:"saml_quota_group_map_removal"`
	SAMLGroupUserGroupMap          string `form:"saml_group_user_group_map" binding:"ValidQuotaGroupMap"`
	SAMLGroupUserGroupMapRemoval   bool   `form:"saml_group_user_group_map_removal"`
}

// Validate validates fields
func (f *AuthenticationForm) Validate(req *http.Request, errs binding.Errors) binding.Errors {
	ctx := context.GetValidateContext(req)
	return middleware.Validate(errs, ctx.Data, f, ctx.Locale)
}
