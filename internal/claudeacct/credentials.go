package claudeacct

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"
)

// sharedCredentialKeys are siblings of claudeAiOauth that belong to the
// machine rather than to an account (MCP and plugin integrations). A slot
// never stores them; activation keeps the live copies. Every other sibling
// (trustedDeviceToken, unknown future keys) is account-owned and travels with
// the slot.
var sharedCredentialKeys = map[string]bool{
	"mcpOAuth":             true,
	"mcpOAuthClientConfig": true,
	"mcpXaaIdp":            true,
	"mcpXaaIdpConfig":      true,
	"pluginSecrets":        true,
}

// Tokens is the part of claudeAiOauth bp reads and rotates. Its String
// methods never reveal token values, so an accidental %v stays safe.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	// ExpiresAt is the access token expiry in Unix milliseconds.
	ExpiresAt int64
	Scopes    []string
}

func (Tokens) String() string   { return "Tokens{[redacted]}" }
func (Tokens) GoString() string { return "Tokens{[redacted]}" }

// Expiry returns the access token expiry, or zero when unknown.
func (t Tokens) Expiry() time.Time {
	if t.ExpiresAt <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(t.ExpiresAt)
}

// Credentials is a parsed credentials file. Unknown keys, inside and outside
// claudeAiOauth, are preserved byte for byte.
type Credentials struct {
	top   map[string]json.RawMessage
	oauth map[string]json.RawMessage
}

func (*Credentials) String() string   { return "Credentials{[redacted]}" }
func (*Credentials) GoString() string { return "Credentials{[redacted]}" }

var errNoOAuth = errors.New("credentials file has no claudeAiOauth login")

// ParseCredentials accepts the credentials file format. It never includes
// file content in its errors.
func ParseCredentials(raw []byte) (*Credentials, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, errors.New("credentials file is not a JSON object")
	}
	inner, ok := top["claudeAiOauth"]
	if !ok {
		return nil, errNoOAuth
	}
	var oauth map[string]json.RawMessage
	if err := json.Unmarshal(inner, &oauth); err != nil || oauth == nil {
		return nil, errNoOAuth
	}
	return &Credentials{top: top, oauth: oauth}, nil
}

// Tokens extracts the token fields.
func (c *Credentials) Tokens() Tokens {
	var t Tokens
	_ = json.Unmarshal(c.oauth["accessToken"], &t.AccessToken)
	_ = json.Unmarshal(c.oauth["refreshToken"], &t.RefreshToken)
	var expires float64
	if json.Unmarshal(c.oauth["expiresAt"], &expires) == nil {
		t.ExpiresAt = int64(expires)
	}
	_ = json.Unmarshal(c.oauth["scopes"], &t.Scopes)
	return t
}

// SetTokens stores rotated tokens, keeping every other claudeAiOauth field.
func (c *Credentials) SetTokens(t Tokens) {
	set := func(key string, value any) {
		data, _ := json.Marshal(value)
		c.oauth[key] = data
	}
	set("accessToken", t.AccessToken)
	set("refreshToken", t.RefreshToken)
	set("expiresAt", t.ExpiresAt)
	if len(t.Scopes) > 0 {
		set("scopes", t.Scopes)
	}
}

// SubscriptionType is shown in listings; it is not secret.
func (c *Credentials) SubscriptionType() string {
	var value string
	_ = json.Unmarshal(c.oauth["subscriptionType"], &value)
	return value
}

// accountOwned drops machine-shared siblings: what a slot stores.
func (c *Credentials) accountOwned() *Credentials {
	out := &Credentials{top: map[string]json.RawMessage{}, oauth: c.oauth}
	for key, value := range c.top {
		if !sharedCredentialKeys[key] {
			out.top[key] = value
		}
	}
	return out
}

// withSharedFrom composes this account's login with the machine-shared keys
// of the live file. Shared keys absent from live stay absent.
func (c *Credentials) withSharedFrom(live *Credentials) *Credentials {
	out := c.accountOwned()
	if live != nil {
		for key, value := range live.top {
			if sharedCredentialKeys[key] {
				out.top[key] = value
			}
		}
	}
	return out
}

// Marshal renders the file with sorted keys. Claude Code reads it as plain
// JSON, so ordering is cosmetic.
func (c *Credentials) Marshal() []byte {
	oauth, _ := marshalSorted(c.oauth)
	top := make(map[string]json.RawMessage, len(c.top))
	for key, value := range c.top {
		top[key] = value
	}
	top["claudeAiOauth"] = oauth
	data, _ := marshalSorted(top)
	return data
}

func marshalSorted(fields map[string]json.RawMessage) (json.RawMessage, error) {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := marshalNoEscape(key)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(fields[key])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalNoEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Identity is the account a login belongs to. Emails are not secret.
type Identity struct {
	Email       string `json:"email"`
	AccountUUID string `json:"accountUuid"`
	OrgUUID     string `json:"orgUuid"`
	OrgName     string `json:"orgName,omitempty"`
}

// Same reports whether two identities are the same account in the same
// organization. An empty account id never matches.
func (i Identity) Same(other Identity) bool {
	return i.AccountUUID != "" && i.AccountUUID == other.AccountUUID && i.OrgUUID == other.OrgUUID
}

// parseOAuthAccount reads the oauthAccount object of the global config.
func parseOAuthAccount(raw json.RawMessage) (Identity, error) {
	var account struct {
		EmailAddress     string `json:"emailAddress"`
		AccountUUID      string `json:"accountUuid"`
		OrganizationUUID string `json:"organizationUuid"`
		OrganizationName string `json:"organizationName"`
	}
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Identity{}, errors.New("no oauthAccount")
	}
	if err := json.Unmarshal(raw, &account); err != nil {
		return Identity{}, errors.New("oauthAccount is not a JSON object")
	}
	if account.AccountUUID == "" {
		return Identity{}, errors.New("oauthAccount has no accountUuid")
	}
	return Identity{Email: account.EmailAddress, AccountUUID: account.AccountUUID, OrgUUID: account.OrganizationUUID, OrgName: account.OrganizationName}, nil
}

// orderedObject is a top-level JSON object whose key order survives a
// rewrite, so splicing oauthAccount leaves the rest of G as Claude Code
// wrote it.
type orderedObject struct {
	keys   []string
	values []json.RawMessage
}

func parseOrderedObject(raw []byte) (*orderedObject, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("not a JSON object")
	}
	obj := &orderedObject{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("invalid object key")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, key)
		obj.values = append(obj.values, value)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON object")
	}
	return obj, nil
}

func (o *orderedObject) get(key string) (json.RawMessage, bool) {
	for i := len(o.keys) - 1; i >= 0; i-- {
		if o.keys[i] == key {
			return o.values[i], true
		}
	}
	return nil, false
}

func (o *orderedObject) set(key string, value json.RawMessage) {
	found := false
	for i := range o.keys {
		if o.keys[i] == key {
			o.values[i], found = value, true
		}
	}
	if !found {
		o.keys = append(o.keys, key)
		o.values = append(o.values, value)
	}
}

// marshal renders with two-space indentation, as Claude Code writes G.
func (o *orderedObject) marshal(trailingNewline bool) ([]byte, error) {
	var compact bytes.Buffer
	compact.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			compact.WriteByte(',')
		}
		name, err := marshalNoEscape(key)
		if err != nil {
			return nil, err
		}
		compact.Write(name)
		compact.WriteByte(':')
		compact.Write(o.values[i])
	}
	compact.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, fmt.Errorf("render global config: %w", err)
	}
	if trailingNewline {
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// spliceOAuthAccount replaces only the oauthAccount key of G. A missing G
// becomes {"oauthAccount": ...}; an unparsable G is refused rather than
// guessed at.
func spliceOAuthAccount(current []byte, exists bool, account json.RawMessage) ([]byte, error) {
	obj := &orderedObject{}
	trailing := false
	if exists && len(bytes.TrimSpace(current)) > 0 {
		parsed, err := parseOrderedObject(current)
		if err != nil {
			return nil, errors.New("global Claude config is not valid JSON; refusing to rewrite it")
		}
		obj = parsed
		trailing = bytes.HasSuffix(current, []byte("\n"))
	}
	obj.set("oauthAccount", account)
	return obj.marshal(trailing)
}
