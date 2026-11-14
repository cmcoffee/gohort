package oidc

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/clientcert"
)

// Store is the part of the program's database sign-in keeps its settings and
// links in. core.Database is one.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	CryptSet(table, key string, value interface{})
	Unset(table, key string)
	Keys(table string) []string
}

// The tables: the settings and their secrets, and the links from an identity
// to a gohort account.
const (
	SettingsTable = "auth_sso"
	LinksTable    = "auth_sso_links"
)

// Keys in SettingsTable.
const (
	settingsKey    = "settings"
	certificateKey = "certificate"   // the certificate and its private key, PEM, encrypted
	secretKey      = "client_secret" // encrypted
)

// What happens when someone signs in whose identity no account is linked to.
const (
	NewPeopleRefuse   = "refuse"   // they link it to an account first, or ask an administrator
	NewPeopleApproval = "approval" // an account is made and waits for approval
	NewPeopleLetIn    = "let_in"   // an account is made, and they are in
)

// How sign-in proves itself to the provider.
const (
	CredentialCertificate = "certificate"
	CredentialSecret      = "secret"
)

// Settings is the deployment's Microsoft sign-in, as an administrator sets
// it. The certificate and the client secret are kept apart, encrypted.
type Settings struct {
	Enabled    bool   `json:"enabled"`
	Tenant     string `json:"tenant"`
	ClientID   string `json:"client_id"`
	Credential string `json:"credential"`
	Label      string `json:"label"`
	NewPeople  string `json:"new_people"`
}

// DefaultLabel is the button's text when none is set.
const DefaultLabel = "Sign in with Microsoft"

// LoadSettings reads the settings, with the defaults filled in.
func LoadSettings(db Store) Settings {
	var s Settings
	if db != nil {
		db.Get(SettingsTable, settingsKey, &s)
	}
	if s.Credential != CredentialSecret {
		s.Credential = CredentialCertificate
	}
	switch s.NewPeople {
	case NewPeopleRefuse, NewPeopleLetIn:
	default:
		s.NewPeople = NewPeopleApproval
	}
	if strings.TrimSpace(s.Label) == "" {
		s.Label = DefaultLabel
	}
	return s
}

// SaveSettings stores them. It refuses settings that cannot sign anyone in
// while turned on.
func SaveSettings(db Store, s Settings) error {
	s.Tenant = strings.TrimSpace(s.Tenant)
	s.ClientID = strings.TrimSpace(s.ClientID)
	s.Label = strings.TrimSpace(s.Label)
	if s.Tenant != "" {
		if err := CheckTenant(s.Tenant); err != nil {
			return err
		}
	}
	if s.ClientID != "" && !guid.MatchString(s.ClientID) {
		return errors.New("the client ID is the app registration's Application (client) ID, a GUID")
	}
	if s.Enabled {
		if err := Ready(db, s); err != nil {
			return fmt.Errorf("not turned on: %v", err)
		}
	}
	db.Set(SettingsTable, settingsKey, s)
	return nil
}

var guid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// CheckTenant refuses anything but one directory's ID. common, organizations
// and consumers let in any Microsoft account, and are not offered.
func CheckTenant(tenant string) error {
	switch strings.ToLower(strings.TrimSpace(tenant)) {
	case "common", "organizations", "consumers":
		return fmt.Errorf("%q signs in anyone's Microsoft account: give your directory's Tenant ID (a GUID)", tenant)
	}
	if !guid.MatchString(strings.TrimSpace(tenant)) {
		return errors.New("the tenant is the directory's Tenant ID, a GUID")
	}
	return nil
}

// Ready says why these settings cannot sign anyone in, or nil.
func Ready(db Store, s Settings) error {
	switch {
	case s.Tenant == "":
		return errors.New("no tenant ID")
	case s.ClientID == "":
		return errors.New("no client ID")
	}
	if err := CheckTenant(s.Tenant); err != nil {
		return err
	}
	if s.Credential == CredentialSecret {
		if LoadSecret(db) == "" {
			return errors.New("no client secret")
		}
		return nil
	}
	c, ok := LoadCertificate(db)
	if !ok {
		return errors.New("no certificate: make one")
	}
	if time.Now().After(c.Expires()) {
		return fmt.Errorf("the certificate expired on %s: make a new one and upload it", c.Expires().Format("Jan 2, 2006"))
	}
	return nil
}

// LoadCertificate is the sign-in certificate, if one was made.
func LoadCertificate(db Store) (clientcert.Cert, bool) {
	var pem string
	if db == nil || !db.Get(SettingsTable, certificateKey, &pem) || pem == "" {
		return clientcert.Cert{}, false
	}
	c, err := clientcert.Parse(pem)
	if err != nil {
		return clientcert.Cert{}, false
	}
	return c, true
}

// SaveCertificate keeps a certificate and its private key, encrypted.
func SaveCertificate(db Store, c clientcert.Cert) {
	db.CryptSet(SettingsTable, certificateKey, c.PEM())
}

// LoadSecret is the client secret, or "".
func LoadSecret(db Store) string {
	var s string
	if db != nil {
		db.Get(SettingsTable, secretKey, &s)
	}
	return s
}

// SaveSecret keeps the client secret, encrypted; "" removes it.
func SaveSecret(db Store, secret string) {
	if strings.TrimSpace(secret) == "" {
		db.Unset(SettingsTable, secretKey)
		return
	}
	db.CryptSet(SettingsTable, secretKey, strings.TrimSpace(secret))
}

// Provider is the settings as a provider to sign in with, the credential
// loaded. redirectURI is the deployment's callback.
func (s Settings) Provider(db Store, issuer, redirectURI string) (Provider, error) {
	if err := Ready(db, s); err != nil {
		return Provider{}, err
	}
	p := Provider{Issuer: issuer, ClientID: s.ClientID, RedirectURI: redirectURI, Tenant: s.Tenant}
	if s.Credential == CredentialSecret {
		p.ClientSecret = LoadSecret(db)
	} else {
		c, _ := LoadCertificate(db)
		p.Certificate = &c
	}
	return p, nil
}

// Link is an identity at the provider tied to a gohort account. It is made
// once, on purpose (by the person, or by a first sign-in that made the
// account), never by matching an email.
type Link struct {
	Username string    `json:"username"`
	LinkedAt time.Time `json:"linked_at"`
	// ShownAs is the identity's username when it was linked, for display.
	ShownAs string `json:"shown_as"`
}

// LinkedAccount is the account an identity is linked to.
func LinkedAccount(db Store, id Identity) (Link, bool) {
	var l Link
	if db == nil || !db.Get(LinksTable, id.Key(), &l) || l.Username == "" {
		return Link{}, false
	}
	return l, true
}

// SetLink ties an identity to an account.
func SetLink(db Store, id Identity, username string, now time.Time) {
	db.Set(LinksTable, id.Key(), Link{Username: username, LinkedAt: now.UTC(), ShownAs: id.Shown()})
}

// AccountLink is one of an account's links, with the key it is stored under.
type AccountLink struct {
	Key string `json:"key"`
	Link
}

// LinksFor are the identities linked to an account, oldest first.
func LinksFor(db Store, username string) []AccountLink {
	var out []AccountLink
	if db == nil {
		return nil
	}
	for _, k := range db.Keys(LinksTable) {
		var l Link
		if db.Get(LinksTable, k, &l) && l.Username == username {
			out = append(out, AccountLink{Key: k, Link: l})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LinkedAt.Before(out[j].LinkedAt) })
	return out
}

// Unlink removes one of an account's links; false when it is not that
// account's.
func Unlink(db Store, username, key string) bool {
	var l Link
	if !db.Get(LinksTable, key, &l) || l.Username != username {
		return false
	}
	db.Unset(LinksTable, key)
	return true
}

// UnlinkAll removes every link to an account, when it is deleted.
func UnlinkAll(db Store, username string) int {
	n := 0
	for _, l := range LinksFor(db, username) {
		db.Unset(LinksTable, l.Key)
		n++
	}
	return n
}
