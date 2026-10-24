// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/organization"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/tests"

	"github.com/crewjam/saml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// samlTestIDP is an identity provider that answers the requests the test passes to it.
type samlTestIDP struct {
	t   *testing.T
	idp *saml.IdentityProvider
	sp  *saml.EntityDescriptor
}

func (p *samlTestIDP) GetServiceProvider(_ *http.Request, id string) (*saml.EntityDescriptor, error) {
	if p.sp == nil || p.sp.EntityID != id {
		return nil, fmt.Errorf("unknown service provider %q", id)
	}
	return p.sp, nil
}

func newSAMLTestIDP(t *testing.T) *samlTestIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	base, _ := url.Parse("https://idp.example.com/")
	p := &samlTestIDP{t: t}
	p.idp = &saml.IdentityProvider{
		Key:                     key,
		Certificate:             cert,
		MetadataURL:             *base.JoinPath("metadata"),
		SSOURL:                  *base.JoinPath("sso"),
		LogoutURL:               *base.JoinPath("slo"),
		ServiceProviderProvider: p,
	}
	return p
}

func (p *samlTestIDP) metadataXML() string {
	data, err := xml.Marshal(p.idp.Metadata())
	require.NoError(p.t, err)
	return string(data)
}

// loadSP reads the SP metadata spoved serves for the source, as an admin would import it into the IdP.
func (p *samlTestIDP) loadSP(name string) {
	resp := MakeRequest(p.t, NewRequest(p.t, "GET", "/user/saml/"+name+"/metadata"), http.StatusOK)
	assert.Equal(p.t, "application/samlmetadata+xml", resp.Header().Get("Content-Type"))
	p.sp = &saml.EntityDescriptor{}
	require.NoError(p.t, xml.Unmarshal(resp.Body.Bytes(), p.sp))
}

func samlSession(nameID, username string, groups ...string) *saml.Session {
	return &saml.Session{
		CreateTime:     time.Now(),
		Index:          "session-" + nameID,
		NameID:         nameID,
		NameIDFormat:   string(saml.PersistentNameIDFormat),
		UserName:       username,
		UserEmail:      username + "@idp.example.com",
		UserCommonName: strings.ToUpper(username),
		Groups:         groups,
	}
}

// answer runs the IdP's SSO endpoint for the authentication request in location and returns its SAMLResponse.
func (p *samlTestIDP) answer(location string, session *saml.Session) string {
	p.t.Helper()
	require.True(p.t, strings.HasPrefix(location, p.idp.SSOURL.String()+"?"), location)
	req, err := saml.NewIdpAuthnRequest(p.idp, httptest.NewRequest("GET", location, nil))
	require.NoError(p.t, err)
	require.NoError(p.t, req.Validate())
	require.NoError(p.t, saml.DefaultAssertionMaker{}.MakeAssertion(req, session))
	form, err := req.PostBinding()
	require.NoError(p.t, err)
	return form.SAMLResponse
}

type samlSessionProvider struct{ session *saml.Session }

func (s samlSessionProvider) GetSession(http.ResponseWriter, *http.Request, *saml.IdpAuthnRequest) *saml.Session {
	return s.session
}

// unsolicited returns an IdP-initiated SAMLResponse.
func (p *samlTestIDP) unsolicited(session *saml.Session) string {
	p.t.Helper()
	p.idp.SessionProvider = samlSessionProvider{session}
	rec := httptest.NewRecorder()
	p.idp.ServeIDPInitiated(rec, httptest.NewRequest("GET", "https://idp.example.com/start", nil), p.sp.EntityID, "")
	require.Equal(p.t, http.StatusOK, rec.Code, rec.Body.String())
	value, ok := NewHTMLParser(p.t, rec.Body).Find(`input[name="SAMLResponse"]`).Attr("value")
	require.True(p.t, ok)
	return value
}

func addSAMLSource(t *testing.T, name string, p *samlTestIDP, extra map[string]string) *auth.Source {
	t.Helper()
	payload := map[string]string{
		"type":                     fmt.Sprintf("%d", auth.SAML),
		"name":                     name,
		"is_active":                "on",
		"saml_idp_metadata":        p.metadataXML(),
		"saml_name_id_format":      string(saml.PersistentNameIDFormat),
		"saml_username_attribute":  "uid",
		"saml_full_name_attribute": "cn",
		"saml_email_attribute":     "mail",
		"saml_group_claim_name":    "eduPersonAffiliation",
	}
	maps.Copy(payload, extra)
	source := addAuthSource(t, payload)
	p.loadSP(name)
	return source
}

// samlStart starts a sign-in on session and returns the IdP URL it sends the browser to.
func samlStart(t *testing.T, session *TestSession, name string) string {
	t.Helper()
	resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/saml/"+name+"/login"), http.StatusSeeOther)
	return test.RedirectURL(resp)
}

// samlSignIn starts a sign-in on session and returns the IdP's answer to it.
func samlSignIn(t *testing.T, session *TestSession, name string, p *samlTestIDP, s *saml.Session) string {
	t.Helper()
	return p.answer(samlStart(t, session, name), s)
}

// postACS posts a SAMLResponse as the IdP's page does: cross-site first, then again from spoved's own page.
func postACS(t *testing.T, session *TestSession, name, samlResponse string) *httptest.ResponseRecorder {
	t.Helper()
	req := NewRequestWithValues(t, "POST", "/user/saml/"+name+"/acs", map[string]string{"SAMLResponse": samlResponse})
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp := session.MakeRequest(t, req, http.StatusOK)
	form := NewHTMLParser(t, resp.Body).Find("form#saml-post")
	action, _ := form.Attr("action")
	assert.Equal(t, "/user/saml/"+name+"/acs", action)
	value, _ := form.Find(`input[name="SAMLResponse"]`).Attr("value")
	assert.Equal(t, samlResponse, value)

	req = NewRequestWithValues(t, "POST", action, map[string]string{"SAMLResponse": value})
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return session.MakeRequest(t, req, http.StatusSeeOther)
}

func signedInAs(t *testing.T, session *TestSession) string {
	t.Helper()
	resp := session.MakeRequest(t, NewRequest(t, "GET", "/user/settings"), NoExpectedStatus)
	if resp.Code != http.StatusOK {
		return ""
	}
	return NewHTMLParser(t, resp.Body).Find(`input[name="name"]`).AttrOr("value", "")
}

func TestSAMLSignIn(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, true)()

	p := newSAMLTestIDP(t)
	source := addSAMLSource(t, "corp", p, map[string]string{
		"saml_group_team_map": `{"dev": {"org3": ["team1"]}}`,
	})
	require.True(t, source.IsSAML())

	t.Run("login page shows the button", func(t *testing.T) {
		resp := MakeRequest(t, NewRequest(t, "GET", "/user/login"), http.StatusOK)
		NewHTMLParser(t, resp.Body).AssertElement(t, `a[href="/user/saml/corp/login"]`, true)
	})

	var samlResponse string
	t.Run("new user is registered and signed in", func(t *testing.T) {
		session := emptyTestSession(t)
		samlResponse = samlSignIn(t, session, "corp", p, samlSession("guid-alice", "alice", "dev"))
		resp := postACS(t, session, "corp", samlResponse)
		assert.Equal(t, "/", test.RedirectURL(resp))
		assert.Equal(t, "alice", signedInAs(t, session))

		u := unittest.AssertExistsAndLoadBean(t, &user_model.User{LowerName: "alice"})
		assert.Equal(t, auth.SAML, u.LoginType)
		assert.Equal(t, source.ID, u.LoginSource)
		assert.Equal(t, "guid-alice", u.LoginName)
		assert.Equal(t, "alice@idp.example.com", u.Email)
		assert.Equal(t, "ALICE", u.FullName)
		isMember, err := organization.IsTeamMember(t.Context(), 3, 2, u.ID)
		require.NoError(t, err)
		assert.True(t, isMember, "group dev maps to org3/team1")
	})

	t.Run("a response to an older request is refused", func(t *testing.T) {
		session := emptyTestSession(t)
		samlStart(t, session, "corp")
		resp := postACS(t, session, "corp", samlResponse)
		assert.Equal(t, "/user/login", test.RedirectURL(resp))
		assert.Empty(t, signedInAs(t, session))
	})

	t.Run("a response for another browser's request is refused", func(t *testing.T) {
		victim := emptyTestSession(t)
		attacker := emptyTestSession(t)
		planted := samlSignIn(t, attacker, "corp", p, samlSession("guid-mallory", "mallory"))
		samlStart(t, victim, "corp")
		resp := postACS(t, victim, "corp", planted)
		assert.Equal(t, "/user/login", test.RedirectURL(resp))
		assert.Empty(t, signedInAs(t, victim))
		unittest.AssertNotExistsBean(t, &user_model.User{LowerName: "mallory"})
	})

	t.Run("existing user signs in again and out through the IdP", func(t *testing.T) {
		session := emptyTestSession(t)
		resp := postACS(t, session, "corp", samlSignIn(t, session, "corp", p, samlSession("guid-alice", "alice", "dev")))
		assert.Equal(t, "/", test.RedirectURL(resp))
		assert.Equal(t, "alice", signedInAs(t, session))

		resp = session.MakeRequest(t, NewRequest(t, "POST", "/user/logout"), http.StatusSeeOther)
		loc := test.RedirectURL(resp)
		require.True(t, strings.HasPrefix(loc, "https://idp.example.com/slo?SAMLRequest="), loc)
		assert.Empty(t, signedInAs(t, session))
	})

	t.Run("without auto registration the user links an account", func(t *testing.T) {
		defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, false)()
		session := emptyTestSession(t)
		resp := postACS(t, session, "corp", samlSignIn(t, session, "corp", p, samlSession("guid-bob", "bob")))
		assert.Equal(t, "/user/link_account", test.RedirectURL(resp))
		unittest.AssertNotExistsBean(t, &user_model.User{LowerName: "bob"})

		session.MakeRequest(t, NewRequest(t, "GET", "/user/link_account"), http.StatusOK)
		req := NewRequestWithValues(t, "POST", "/user/link_account_signin", map[string]string{"user_name": "user2", "password": userPassword})
		session.MakeRequest(t, req, http.StatusSeeOther)
		assert.Equal(t, "user2", signedInAs(t, session))
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user2"})
		linked, err := db.GetEngine(t.Context()).Exist(&user_model.ExternalLoginUser{ExternalID: "guid-bob", LoginSourceID: source.ID, UserID: user2.ID})
		require.NoError(t, err)
		assert.True(t, linked)

		// the linked account signs in through the IdP from now on
		other := emptyTestSession(t)
		resp = postACS(t, other, "corp", samlSignIn(t, other, "corp", p, samlSession("guid-bob", "bob")))
		assert.Equal(t, "/", test.RedirectURL(resp))
		assert.Equal(t, "user2", signedInAs(t, other))
	})

	t.Run("unknown source is not found", func(t *testing.T) {
		MakeRequest(t, NewRequest(t, "GET", "/user/saml/nope/login"), http.StatusNotFound)
		MakeRequest(t, NewRequest(t, "GET", "/user/saml/nope/metadata"), http.StatusNotFound)
	})
}

func TestSAMLIDPInitiated(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.OAuth2Client.EnableAutoRegistration, true)()

	p := newSAMLTestIDP(t)
	addSAMLSource(t, "corp", p, nil)
	session := emptyTestSession(t)
	postACS(t, session, "corp", samlSignIn(t, session, "corp", p, samlSession("guid-alice", "alice")))
	require.Equal(t, "alice", signedInAs(t, session))

	t.Run("refused unless allowed", func(t *testing.T) {
		session := emptyTestSession(t)
		resp := postACS(t, session, "corp", p.unsolicited(samlSession("guid-alice", "alice")))
		assert.Equal(t, "/user/login", test.RedirectURL(resp))
		assert.Empty(t, signedInAs(t, session))
	})

	addSAMLSource(t, "open", p, map[string]string{"saml_allow_idp_initiated": "on"})
	session = emptyTestSession(t)
	postACS(t, session, "open", samlSignIn(t, session, "open", p, samlSession("guid-carol", "carol")))
	require.Equal(t, "carol", signedInAs(t, session))

	t.Run("signs in a linked user once", func(t *testing.T) {
		session := emptyTestSession(t)
		samlResponse := p.unsolicited(samlSession("guid-carol", "carol"))
		resp := postACS(t, session, "open", samlResponse)
		assert.Equal(t, "/", test.RedirectURL(resp))
		assert.Equal(t, "carol", signedInAs(t, session))

		replay := emptyTestSession(t)
		resp = postACS(t, replay, "open", samlResponse)
		assert.Equal(t, "/user/login", test.RedirectURL(resp))
		assert.Empty(t, signedInAs(t, replay))
	})

	t.Run("never registers or links", func(t *testing.T) {
		session := emptyTestSession(t)
		resp := postACS(t, session, "open", p.unsolicited(samlSession("guid-dave", "dave")))
		assert.Equal(t, "/user/login", test.RedirectURL(resp))
		assert.Empty(t, signedInAs(t, session))
		unittest.AssertNotExistsBean(t, &user_model.User{LowerName: "dave"})
	})
}

func TestSAMLAdminForm(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	p := newSAMLTestIDP(t)
	source := addSAMLSource(t, "corp", p, nil)
	session := loginUser(t, "user1")

	resp := session.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/admin/auths/%d", source.ID)), http.StatusOK)
	doc := NewHTMLParser(t, resp.Body)
	assert.Contains(t, doc.Find("form").Text(), setting.AppURL+"user/saml/corp/acs")

	t.Run("invalid metadata is refused", func(t *testing.T) {
		req := NewRequestWithValues(t, "POST", "/admin/auths/new", map[string]string{
			"type":              fmt.Sprintf("%d", auth.SAML),
			"name":              "broken",
			"is_active":         "on",
			"saml_idp_metadata": "<not-metadata/>",
		})
		session.MakeRequest(t, req, http.StatusOK)
		unittest.AssertNotExistsBean(t, &auth.Source{Name: "broken"})
	})

	t.Run("a private key is stored encrypted and never shown", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		require.NoError(t, err)
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

		signed := addSAMLSource(t, "signed", p, map[string]string{
			"saml_sp_certificate": certPEM,
			"saml_sp_private_key": keyPEM,
			"saml_sign_requests":  "on",
		})
		raw, err := signed.Cfg.ToDB()
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "PRIVATE KEY")

		resp := session.MakeRequest(t, NewRequest(t, "GET", fmt.Sprintf("/admin/auths/%d", signed.ID)), http.StatusOK)
		assert.NotContains(t, resp.Body.String(), "PRIVATE KEY")

		// the SP metadata names the key, and requests are signed
		assert.NotNil(t, p.sp.SPSSODescriptors[0].KeyDescriptors)
		assert.Contains(t, samlStart(t, emptyTestSession(t), "signed"), "&Signature=")
	})
}
