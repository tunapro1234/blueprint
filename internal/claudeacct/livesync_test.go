package claudeacct

import (
	"testing"
	"time"
)

// Claude Code rotates the active account's refresh token on its own. The
// slot copy must follow, or replacing the live file (a /login for another
// account) loses the only valid token and the slot dies (7 Nov 2026).
func TestDaemonPassSyncsRefreshedLiveLogin(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(2) // slot 2 live
	rotated := f.account(2)
	rotated.access, rotated.refresh = fakeToken("access", 222), fakeToken("refresh", 222)
	rotated.expiresAt = f.now.Add(8 * time.Hour).UnixMilli()
	f.writeLive(rotated)

	if _, err := f.m.PollOnce(testCtx(t), time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.slotTokens(2).RefreshToken; got != rotated.refresh {
		t.Fatalf("slot 2 was not synced to the rotated live login")
	}

	// Another account's /login replaces the live file; slot 2 keeps the
	// rotated token, and the next pass leaves both slots as they are.
	f.writeLive(f.account(3))
	if _, err := f.m.AutoOnce(testCtx(t), AutoOptions{Policy: AutoPolicy{}, PollEvery: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if got := f.slotTokens(2).RefreshToken; got != rotated.refresh {
		t.Fatalf("slot 2 lost its rotated token after the live login changed")
	}
	if got := f.slotTokens(1).RefreshToken; got != f.account(1).refresh {
		t.Fatalf("slot 1 was touched")
	}
}

func TestLiveSyncNeverWritesAnOlderLogin(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(1)
	older := f.account(1)
	older.refresh = fakeToken("refresh", 99)
	older.expiresAt = f.now.Add(time.Hour).UnixMilli()
	f.writeLive(older)
	if _, err := f.m.PollOnce(testCtx(t), time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.slotTokens(1).RefreshToken; got != f.account(1).refresh {
		t.Fatalf("an older live login overwrote the newer stored copy")
	}
}

func TestDryRunAutoDoesNotSync(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(1)
	rotated := f.account(1)
	rotated.refresh = fakeToken("refresh", 111)
	rotated.expiresAt = f.now.Add(8 * time.Hour).UnixMilli()
	f.writeLive(rotated)
	if _, err := f.m.AutoOnce(testCtx(t), AutoOptions{Policy: AutoPolicy{}, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.slotTokens(1).RefreshToken; got != f.account(1).refresh {
		t.Fatalf("dry run wrote the slot")
	}
}
