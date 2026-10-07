package claudeacct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	f := newFixture(t, false)
	f.writeLive(f.account(1))
	res, err := f.m.Add(testCtx(t), 0, "work")
	if err != nil {
		t.Fatal(err)
	}
	if res.Slot.Number != 1 || res.Updated || res.Slot.Email != "user1@example.com" || res.Slot.OrgName != "Org org-1" || res.Slot.Alias != "work" {
		t.Fatalf("add result %+v", res)
	}
	root := f.m.Store.Root()
	for _, dir := range []string{root, filepath.Join(root, "slots"), filepath.Join(root, "slots", "1")} {
		assertMode(t, dir, 0o700)
	}
	for _, file := range []string{"accounts.json", "slots/1/credentials.json", "slots/1/oauthAccount.json"} {
		assertMode(t, filepath.Join(root, file), 0o600)
	}
	accounts, err := f.m.Store.Load()
	if err != nil || len(accounts.Slots) != 1 || accounts.Active != 1 {
		t.Fatalf("load %+v %v", accounts, err)
	}
	if err := f.m.Store.Save(accounts); err != nil {
		t.Fatal(err)
	}
	again, _ := f.m.Store.Load()
	a, _ := json.Marshal(accounts)
	b, _ := json.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Fatalf("round trip changed:\n%s\n%s", a, b)
	}
	if got := f.slotTokens(1); got.AccessToken != f.account(1).access || got.RefreshToken != f.account(1).refresh {
		t.Fatal("stored tokens differ from the live login")
	}
	// Slots keep only the account's login, never machine-shared keys.
	data, _ := os.ReadFile(filepath.Join(root, "slots/1/credentials.json"))
	if strings.Contains(string(data), "mcpOAuth") || !strings.Contains(string(data), "rateLimitTier") {
		t.Fatalf("slot credentials kept the wrong keys: %s", strings.ReplaceAll(string(data), tokenMarker, "T"))
	}
	accountsJSON, _ := os.ReadFile(filepath.Join(root, "accounts.json"))
	assertNoToken(t, "accounts.json", string(accountsJSON))
}

func TestAddDeduplicatesSameAccountAndOrg(t *testing.T) {
	f := newFixture(t, true)
	f.addAccounts(1)
	rotated := f.account(1)
	rotated.access, rotated.refresh = fakeToken("access", 11), fakeToken("refresh", 11)
	f.writeLive(rotated)
	res, err := f.m.Add(testCtx(t), 0, "")
	if err != nil || !res.Updated || res.Slot.Number != 1 {
		t.Fatalf("re-add %+v %v", res, err)
	}
	if f.slotTokens(1).RefreshToken != rotated.refresh {
		t.Fatal("re-add did not refresh the stored login")
	}
	if _, err := f.m.Add(testCtx(t), 3, ""); err == nil || !strings.Contains(err.Error(), "already stored in slot 1") {
		t.Fatalf("re-add into another slot: %v", err)
	}
	// Same account in another organization is a different slot.
	other := f.account(1)
	other.org = "org-other"
	f.writeLive(other)
	if res, err := f.m.Add(testCtx(t), 0, ""); err != nil || res.Updated || res.Slot.Number != 2 {
		t.Fatalf("other org %+v %v", res, err)
	}
	f.writeLive(f.account(3))
	if _, err := f.m.Add(testCtx(t), 1, ""); err == nil {
		t.Fatal("added a different account over slot 1")
	}
	if res, err := f.m.Add(testCtx(t), 7, "seven"); err != nil || res.Slot.Number != 7 {
		t.Fatalf("explicit slot %+v %v", res, err)
	}
	if _, err := f.m.SetAlias(testCtx(t), 1, "seven"); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	accounts, _ := f.m.Store.Load()
	if len(accounts.Slots) != 3 {
		t.Fatalf("slots %+v", accounts.Slots)
	}
}

func TestAddRefusesLoginWithoutIdentityOrRefreshToken(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.m.Add(testCtx(t), 0, ""); err == nil {
		t.Fatal("added without a login")
	}
	l := f.account(1)
	l.refresh = ""
	f.writeLive(l)
	if _, err := f.m.Add(testCtx(t), 0, ""); err == nil {
		t.Fatal("added a login without refresh token")
	}
}

func TestSwitchWritesLiveFilesAndPreservesOtherKeys(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	if err := os.Chmod(f.paths.GlobalConfig, 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.paths.GlobalConfig)
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1", Cooldown: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if res.From != 2 || res.To.Number != 1 || !res.Captured || res.Displaced != "" || res.Refreshed {
		t.Fatalf("result %+v", res)
	}
	if tok := f.liveTokens(); tok.AccessToken != f.account(1).access || tok.RefreshToken != f.account(1).refresh {
		t.Fatal("live credentials are not slot 1's")
	}
	if id := f.liveIdentity(); id.AccountUUID != "acct-1" || id.Email != "user1@example.com" {
		t.Fatalf("live identity %+v", id)
	}
	assertMode(t, f.paths.CredentialsFile(), 0o600)
	assertMode(t, f.paths.GlobalConfig, 0o640)
	creds, _ := os.ReadFile(f.paths.CredentialsFile())
	if !strings.Contains(string(creds), "machine-mcp") {
		t.Fatal("machine-shared MCP credentials were dropped")
	}
	after, _ := os.ReadFile(f.paths.GlobalConfig)
	keyOrder := func(data []byte) []string {
		obj, err := parseOrderedObject(data)
		if err != nil {
			t.Fatal(err)
		}
		return obj.keys
	}
	if strings.Join(keyOrder(before), ",") != strings.Join(keyOrder(after), ",") || !bytes.HasSuffix(after, []byte("}\n")) {
		t.Fatalf("global config keys reordered:\n%s", after)
	}
	for _, keep := range []string{`"numStartups": 42`, `"/x"`, `"zLast": true`, `"displayName": "User"`} {
		if !bytes.Contains(after, []byte(keep)) {
			t.Fatalf("global config lost %s:\n%s", keep, after)
		}
	}
	accounts, _ := f.m.Store.Load()
	state, _ := f.m.Store.LoadAutoState()
	if accounts.Active != 1 || state.LastFrom != 2 || state.LastTo != 1 || !state.LastSwitchAt.Equal(f.now) {
		t.Fatalf("recorded %+v %+v", accounts.Active, state)
	}
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "user1@example.com"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("switch to the active slot: %v", err)
	}
	// Claude Code's lock directories are gone afterwards.
	for _, lock := range []string{f.paths.refreshLock(), f.paths.legacyLock(), f.paths.configLock()} {
		if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("lock %s left behind", lock)
		}
	}
}

func TestSwitchCreatesMissingGlobalConfig(t *testing.T) {
	f := newFixture(t, true)
	f.addAccounts(2)
	if err := os.Remove(f.paths.GlobalConfig); err != nil {
		t.Fatal(err)
	}
	// Without G the live login has no identity: it is kept as a displaced copy.
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"})
	if err != nil || res.Displaced == "" || res.Captured {
		t.Fatalf("%+v %v", res, err)
	}
	assertMode(t, f.paths.GlobalConfig, 0o600)
	if f.liveIdentity().AccountUUID != "acct-1" {
		t.Fatal("global config not created")
	}
}

func TestCaptureBackOfRotatedLiveToken(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	// Claude Code refreshed account 2 while it was live.
	rotated := f.account(2)
	rotated.access, rotated.refresh = fakeToken("access", 22), fakeToken("refresh", 22)
	f.writeLive(rotated)
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"}); err != nil {
		t.Fatal(err)
	}
	if got := f.slotTokens(2); got.RefreshToken != rotated.refresh || got.AccessToken != rotated.access {
		t.Fatal("rotated live token was not captured into slot 2")
	}
	// Switching back activates the captured token, not the stale one.
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "2"}); err != nil {
		t.Fatal(err)
	}
	if f.liveTokens().RefreshToken != rotated.refresh {
		t.Fatal("switching back restored a stale refresh token")
	}
	if refreshes, _ := f.api.calls(); len(refreshes) != 0 {
		t.Fatalf("switch refreshed valid tokens: %d calls", len(refreshes))
	}
}

func TestIdentityMismatchRefusesCaptureAndKeepsDisplacedCopy(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	slot1, slot2 := f.slotTokens(1), f.slotTokens(2)
	// The user logged in to an account bp does not manage; accounts.json
	// still says slot 2 is active.
	stranger := f.account(9)
	f.writeLive(stranger)
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Captured || res.Displaced == "" {
		t.Fatalf("result %+v", res)
	}
	if f.slotTokens(2).RefreshToken != slot2.RefreshToken || f.slotTokens(1).RefreshToken != slot1.RefreshToken {
		t.Fatal("a foreign login was captured into a slot")
	}
	assertMode(t, res.Displaced, 0o600)
	assertMode(t, filepath.Dir(res.Displaced), 0o700)
	data, _ := os.ReadFile(res.Displaced)
	var copy struct {
		Reason       string          `json:"reason"`
		Credentials  json.RawMessage `json:"credentials"`
		OAuthAccount json.RawMessage `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	creds, err := ParseCredentials(copy.Credentials)
	if err != nil || creds.Tokens().RefreshToken != stranger.refresh || !strings.Contains(string(copy.OAuthAccount), "acct-9") {
		t.Fatal("displaced copy does not hold the replaced login")
	}

	// A live login matching a different slot than accounts.json records is
	// captured into the slot its identity matches, never the recorded one.
	f.writeLive(f.account(2))
	rotated := f.account(2)
	rotated.refresh = fakeToken("refresh", 23)
	f.writeLive(rotated)
	accounts, _ := f.m.Store.Load()
	accounts.Active = 1
	_ = f.m.Store.Save(accounts)
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"}); err != nil {
		t.Fatal(err)
	}
	if f.slotTokens(1).RefreshToken != slot1.RefreshToken || f.slotTokens(2).RefreshToken != rotated.refresh {
		t.Fatal("capture-back used the recorded slot instead of the live identity")
	}
}

func TestSwitchRollsBackWhenGlobalWriteFails(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	credsBefore, _ := os.ReadFile(f.paths.CredentialsFile())
	globalBefore, _ := os.ReadFile(f.paths.GlobalConfig)
	f.m.writeLive = func(path string, data []byte, mode os.FileMode) error {
		if path == f.paths.GlobalConfig {
			return errors.New("disk full")
		}
		return writeAtomic(path, data, mode)
	}
	_, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"})
	if err == nil || !strings.Contains(err.Error(), "previous login restored") {
		t.Fatalf("err %v", err)
	}
	credsAfter, _ := os.ReadFile(f.paths.CredentialsFile())
	globalAfter, _ := os.ReadFile(f.paths.GlobalConfig)
	if !bytes.Equal(credsBefore, credsAfter) || !bytes.Equal(globalBefore, globalAfter) {
		t.Fatal("live files not restored after a failed switch")
	}
	assertMode(t, f.paths.CredentialsFile(), 0o600)
	accounts, _ := f.m.Store.Load()
	if accounts.Active != 2 {
		t.Fatalf("active recorded as %d after rollback", accounts.Active)
	}
	assertNoToken(t, "rollback error", err.Error())
}

func TestClaudeLockContention(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	lock := f.paths.refreshLock()
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"})
	var busy *LockBusyError
	if !errors.As(err, &busy) || !strings.Contains(err.Error(), "Claude Code is refreshing credentials") {
		t.Fatalf("fresh lock: %v", err)
	}
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("waited %v for a 300ms lock timeout", waited)
	}
	if f.liveIdentity().AccountUUID != "acct-2" {
		t.Fatal("switched while Claude Code held its lock")
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("bp removed a fresh lock it did not own")
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"}); err != nil {
		t.Fatalf("stale lock not taken over: %v", err)
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("taken-over lock not released")
	}
	// The global config lock is stale after 10s, not 60s.
	if err := os.Mkdir(f.paths.configLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	mid := time.Now().Add(-20 * time.Second)
	_ = os.Chtimes(f.paths.configLock(), mid, mid)
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "2"}); err != nil {
		t.Fatalf("stale config lock not taken over: %v", err)
	}
}

func TestSwitchRefreshesExpiringTargetAndPersistsRotation(t *testing.T) {
	f := newFixture(t, false)
	expiring := f.account(1)
	expiring.expiresAt = f.now.Add(2 * time.Minute).UnixMilli()
	f.writeLive(expiring)
	if _, err := f.m.Add(testCtx(t), 0, ""); err != nil {
		t.Fatal(err)
	}
	f.writeLive(f.account(2))
	if _, err := f.m.Add(testCtx(t), 0, ""); err != nil {
		t.Fatal(err)
	}
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"})
	if err != nil || !res.Refreshed {
		t.Fatalf("%+v %v", res, err)
	}
	refreshes, _ := f.api.calls()
	if len(refreshes) != 1 || refreshes[0] != expiring.refresh {
		t.Fatalf("refresh calls %d", len(refreshes))
	}
	want := fakeToken("refresh", 101)
	if f.slotTokens(1).RefreshToken != want || f.liveTokens().RefreshToken != want || f.liveTokens().AccessToken != fakeToken("access", 101) {
		t.Fatal("rotated refresh token not persisted and activated")
	}
	if exp := f.liveTokens().Expiry(); !exp.Equal(f.now.Add(8 * time.Hour)) {
		t.Fatalf("expiry %v", exp)
	}
}

func TestOverviewRefreshesOnlyInactiveExpiringLogins(t *testing.T) {
	f := newFixture(t, false)
	expiring := f.account(1)
	expiring.expiresAt = f.now.Add(time.Minute).UnixMilli()
	f.writeLive(expiring)
	f.m.Add(testCtx(t), 0, "")
	// The live account's token is expired: bp must not refresh it.
	live := f.account(2)
	live.expiresAt = f.now.Add(-time.Minute).UnixMilli()
	f.writeLive(live)
	f.m.Add(testCtx(t), 0, "")
	f.api.setUsage(fakeToken("access", 101), 10, 20)
	o, err := f.m.Overview(testCtx(t), OverviewOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	refreshes, usage := f.api.calls()
	if len(refreshes) != 1 || refreshes[0] != expiring.refresh || len(usage) != 1 {
		t.Fatalf("refreshes=%d usage=%d", len(refreshes), len(usage))
	}
	if f.slotTokens(1).RefreshToken != fakeToken("refresh", 101) {
		t.Fatal("rotated token not persisted")
	}
	if f.liveTokens().RefreshToken != live.refresh {
		t.Fatal("the live login was touched")
	}
	out := render(o)
	if !strings.Contains(out, "token: token expired") || !strings.Contains(out, "5h 10%") || !strings.Contains(out, "7d 20%") || !strings.Contains(out, "* slot 2") {
		t.Fatalf("listing:\n%s", out)
	}
	assertNoToken(t, "list", out)
	// Fresh cache is not refetched.
	if _, err := f.m.Overview(testCtx(t), OverviewOptions{Fetch: true}); err != nil {
		t.Fatal(err)
	}
	if _, usage2 := f.api.calls(); len(usage2) != 1 {
		t.Fatalf("fresh usage refetched: %d", len(usage2))
	}
}

func TestActiveUsageUsesLiveToken(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(1)
	rotated := f.account(1)
	rotated.access = fakeToken("access", 55)
	f.writeLive(rotated)
	f.api.setUsage(rotated.access, 91, 30)
	o, err := f.m.Overview(testCtx(t), OverviewOptions{Fetch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, usage := f.api.calls(); len(usage) != 1 || usage[0] != rotated.access {
		t.Fatal("active usage did not use the live token")
	}
	if v, _ := o.Slots[0].LastUsage.Usage.Max(f.now); v != 91 {
		t.Fatalf("max %v", v)
	}
}

func TestInvalidGrantMarksDeadAndStrategiesSkipIt(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(3)
	// Slot 1 has the lowest usage but its refresh token was revoked.
	accounts, _ := f.m.Store.Load()
	creds, _ := f.m.Store.ReadCredentials(1)
	tok := creds.Tokens()
	tok.ExpiresAt = f.now.Add(-time.Hour).UnixMilli()
	creds.SetTokens(tok)
	_ = f.m.Store.WriteCredentials(1, creds)
	_ = f.m.Store.Save(accounts)
	f.setCachedUsage(1, 5, 5)
	f.setCachedUsage(2, 40, 40)
	f.setCachedUsage(3, 95, 95)
	f.api.refreshFail[tok.RefreshToken] = [2]string{"400", "invalid_grant"}
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Strategy: StrategyBest})
	if err != nil {
		t.Fatal(err)
	}
	if res.To.Number != 2 {
		t.Fatalf("switched to %d", res.To.Number)
	}
	accounts, _ = f.m.Store.Load()
	if s := accounts.Slot(1); !s.Dead || s.DeadReason != "invalid_grant" {
		t.Fatalf("slot 1 %+v", s)
	}
	live, _ := f.m.readLive()
	for _, strategy := range []string{StrategyRotation, StrategyBest, StrategyNextAvailable} {
		for _, n := range strategyCandidates(accounts, live, strategy, f.now) {
			if n == 1 {
				t.Fatalf("%q picked the dead slot", strategy)
			}
		}
	}
	if d := Decide(accounts, 3, AutoState{}, AutoPolicy{Threshold: 90}, f.now); d.Action == AutoSwitch && d.To == 1 {
		t.Fatal("auto picked the dead slot")
	}
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "1"}); err == nil || !strings.Contains(err.Error(), "dead") {
		t.Fatalf("explicit switch to a dead slot: %v", err)
	}
	o, _ := f.m.Overview(testCtx(t), OverviewOptions{})
	out := render(o)
	if !strings.Contains(out, "refresh dead") {
		t.Fatalf("listing:\n%s", out)
	}
	assertNoToken(t, "dead listing", out)
}

func TestInvalidClientIsSystemicNotDead(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(3)
	for _, n := range []int{1, 2} {
		creds, _ := f.m.Store.ReadCredentials(n)
		tok := creds.Tokens()
		tok.ExpiresAt = f.now.Add(-time.Hour).UnixMilli()
		creds.SetTokens(tok)
		_ = f.m.Store.WriteCredentials(n, creds)
		f.api.refreshFail[tok.RefreshToken] = [2]string{"401", "invalid_client"}
	}
	_, err := f.m.Switch(testCtx(t), SwitchOptions{})
	var refreshErr *RefreshError
	if !errors.As(err, &refreshErr) || refreshErr.Kind != "invalid_client" {
		t.Fatalf("err %v", err)
	}
	if refreshes, _ := f.api.calls(); len(refreshes) != 1 {
		t.Fatalf("kept trying after a systemic failure: %d", len(refreshes))
	}
	accounts, _ := f.m.Store.Load()
	for _, s := range accounts.Slots {
		if s.Dead {
			t.Fatalf("slot %d marked dead on invalid_client", s.Number)
		}
	}
	assertNoToken(t, "invalid_client error", err.Error())
}

func TestStrategiesAndSelectors(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(4) // slot 4 live
	f.setCachedUsage(1, 100, 10)
	f.setCachedUsage(2, 50, 60)
	f.setCachedUsage(3, 20, 30)
	if _, err := f.m.SetDisabled(testCtx(t), 3, true); err != nil {
		t.Fatal(err)
	}
	accounts, _ := f.m.Store.Load()
	live, _ := f.m.readLive()
	check := func(strategy string, want ...int) {
		t.Helper()
		got := strategyCandidates(accounts, live, strategy, f.now)
		if len(got) != len(want) {
			t.Fatalf("%q: %v want %v", strategy, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%q: %v want %v", strategy, got, want)
			}
		}
	}
	check(StrategyRotation, 1, 2)
	check(StrategyBest, 2, 1)
	check(StrategyNextAvailable, 2)
	res, err := f.m.Switch(testCtx(t), SwitchOptions{Strategy: StrategyNextAvailable, DryRun: true})
	if err != nil || res.To.Number != 2 || !res.DryRun || f.liveIdentity().AccountUUID != "acct-4" {
		t.Fatalf("dry run %+v %v", res, err)
	}
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "3"}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("switch to disabled: %v", err)
	}
	if _, err := f.m.SetAlias(testCtx(t), 2, "spare"); err != nil {
		t.Fatal(err)
	}
	if res, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "SPARE"}); err != nil || res.To.Number != 2 {
		t.Fatalf("alias switch %+v %v", res, err)
	}
	// Rotation continues after the new active slot and wraps.
	accounts, _ = f.m.Store.Load()
	live, _ = f.m.readLive()
	check(StrategyRotation, 4, 1)
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Selector: "nobody@example.com"}); err == nil {
		t.Fatal("unknown email accepted")
	}
	if _, err := f.m.Switch(testCtx(t), SwitchOptions{Strategy: "random"}); err == nil {
		t.Fatal("unknown strategy accepted")
	}
}

func TestRemoveRetiresFilesAndRefusesActive(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2)
	if _, err := f.m.Remove(testCtx(t), 2); err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("removed the active slot: %v", err)
	}
	removed, err := f.m.Remove(testCtx(t), 1)
	if err != nil || removed.Number != 1 {
		t.Fatalf("%+v %v", removed, err)
	}
	accounts, _ := f.m.Store.Load()
	if accounts.Slot(1) != nil {
		t.Fatal("slot 1 still listed")
	}
	matches, _ := filepath.Glob(filepath.Join(f.m.Store.Root(), "removed", "*-slot1", "credentials.json"))
	if len(matches) != 1 {
		t.Fatal("removed slot files were not retired")
	}
	if _, err := f.m.Remove(testCtx(t), 9); err == nil {
		t.Fatal("removed a missing slot")
	}
}

func TestStoreLockSerializes(t *testing.T) {
	f := newFixture(t, false)
	unlock, err := f.m.Store.Lock(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := f.m.Store.Lock(ctx); err == nil {
		t.Fatal("second lock taken")
	}
	unlock()
	again, err := f.m.Store.Lock(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	again()
}
