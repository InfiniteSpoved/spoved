// Copyright 2026 The spoved contributors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package saml

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"forgejo.org/modules/cache"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/markbates/goth"
)

// MaxResponseSize bounds the decoded SAMLResponse.
const MaxResponseSize = 1 << 20

// ErrInvalidResponse is returned for any response that is refused; the cause is only logged.
type ErrInvalidResponse struct {
	Cause error
}

func (err ErrInvalidResponse) Error() string {
	return "invalid SAML response: " + err.Cause.Error()
}

func (err ErrInvalidResponse) Unwrap() error {
	return err.Cause
}

func invalid(format string, args ...any) error {
	return ErrInvalidResponse{Cause: fmt.Errorf(format, args...)}
}

// Assertion is what a verified response says about the user.
type Assertion struct {
	User         goth.User
	NameID       string
	NameIDFormat string
	SessionIndex string
}

// ParseResponse verifies a base64 SAMLResponse posted to the ACS and returns the user it asserts. requestID is the ID
// of the AuthnRequest this browser sent ("" if none): without it only an IdP-initiated sign-in can succeed, and only
// when the source allows it.
func (source *Source) ParseResponse(sp *saml.ServiceProvider, samlResponse, requestID string) (*Assertion, error) {
	if base64.StdEncoding.DecodedLen(len(samlResponse)) > MaxResponseSize {
		return nil, invalid("the response is larger than %d bytes", MaxResponseSize)
	}
	raw, err := base64.StdEncoding.DecodeString(samlResponse)
	if err != nil {
		return nil, invalid("the response is not base64")
	}
	if requestID == "" && !source.AllowIdPInitiated {
		return nil, invalid("no sign-in was started in this browser and IdP-initiated sign-in is off")
	}
	if source.RequireEncryptedAssertions {
		if err := checkNoPlainAssertion(raw); err != nil {
			return nil, err
		}
	}

	var ids []string
	if requestID != "" {
		ids = []string{requestID}
	}
	a, err := sp.ParseXMLResponse(raw, ids, sp.AcsURL)
	if err != nil {
		var ire *saml.InvalidResponseError
		if errors.As(err, &ire) && ire.PrivateErr != nil {
			err = ire.PrivateErr
		}
		return nil, ErrInvalidResponse{Cause: err}
	}
	if err := checkReplay(source.authSource.ID, a); err != nil {
		return nil, err
	}
	return source.toAssertion(a)
}

// checkNoPlainAssertion refuses a response that carries an unencrypted Assertion: the library would accept it
// alongside, or instead of, an EncryptedAssertion.
func checkNoPlainAssertion(raw []byte) error {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil || doc.Root() == nil {
		return invalid("the response is not XML")
	}
	for _, el := range doc.Root().ChildElements() {
		if el.Tag == "Assertion" {
			return invalid("the response holds an unencrypted assertion and this source requires encryption")
		}
	}
	return nil
}

var replayMu sync.Mutex

// checkReplay refuses an assertion ID seen before, until the assertion expires. The cache is per process unless the
// instance uses a shared cache (redis); requests started in the browser are also bound to its session.
func checkReplay(sourceID int64, a *saml.Assertion) error {
	if a.ID == "" {
		return invalid("the assertion has no ID")
	}
	c := cache.GetCache()
	if c == nil {
		return invalid("no cache to record the assertion ID")
	}
	sum := sha256.Sum256([]byte(a.ID))
	key := fmt.Sprintf("saml_assertion_%d_%s", sourceID, hex.EncodeToString(sum[:]))

	ttl := time.Until(a.Conditions.NotOnOrAfter.Add(saml.MaxClockSkew))
	ttl = min(max(ttl, time.Minute), 24*time.Hour)

	replayMu.Lock()
	defer replayMu.Unlock()
	if c.IsExist(key) {
		return invalid("the assertion was used before")
	}
	if err := c.Put(key, 1, int64(ttl/time.Second)); err != nil {
		return invalid("record the assertion ID: %v", err)
	}
	return nil
}

// attributes returns the values of each attribute, by name and by friendly name.
func attributes(a *saml.Assertion) map[string][]string {
	attrs := map[string][]string{}
	for _, st := range a.AttributeStatements {
		for _, attr := range st.Attributes {
			var values []string
			for _, v := range attr.Values {
				if v.NameID != nil {
					values = append(values, v.NameID.Value)
				} else {
					values = append(values, v.Value)
				}
			}
			for _, name := range []string{attr.Name, attr.FriendlyName} {
				if name != "" {
					attrs[name] = append(attrs[name], values...)
				}
			}
		}
	}
	return attrs
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// maxExternalIDLength is the length of external_login_user.external_id and user.login_name.
const maxExternalIDLength = 255

func (source *Source) toAssertion(a *saml.Assertion) (*Assertion, error) {
	if a.Subject == nil || a.Subject.NameID == nil || a.Subject.NameID.Value == "" {
		return nil, invalid("the assertion has no NameID")
	}
	attrs := attributes(a)
	nameID := a.Subject.NameID

	userID := nameID.Value
	if source.UserIDAttribute != "" {
		values := attrs[source.UserIDAttribute]
		if len(values) != 1 || values[0] == "" {
			return nil, invalid("the attribute %q must hold exactly one user ID, it has %d values", source.UserIDAttribute, len(values))
		}
		userID = values[0]
	}
	if len(userID) > maxExternalIDLength {
		return nil, invalid("the user ID is longer than %d bytes", maxExternalIDLength)
	}

	rawData := make(map[string]any, len(attrs)+1)
	for name, values := range attrs {
		rawData[name] = values
	}
	username := first(attrs[source.UsernameAttribute])
	rawData["preferred_username"] = username

	email := first(attrs[source.EmailAttribute])
	if email == "" && nameID.Format == string(saml.EmailAddressNameIDFormat) {
		email = nameID.Value
	}

	result := &Assertion{
		User: goth.User{
			Provider: source.authSource.Name,
			UserID:   userID,
			NickName: username,
			Name:     first(attrs[source.FullNameAttribute]),
			Email:    email,
			RawData:  rawData,
		},
		NameID:       nameID.Value,
		NameIDFormat: nameID.Format,
	}
	if len(a.AuthnStatements) > 0 {
		result.SessionIndex = a.AuthnStatements[0].SessionIndex
	}
	return result, nil
}
