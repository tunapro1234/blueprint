// console_render_seed.mjs — seed a throwaway BP_HOME with the data the Usage
// page needs to render its full cost view: an owner usage/pricing block, four
// Team "premium seat" accounts with five-hour readings, and a token store with
// real per-model usage. It writes only inside the temp home the render harness
// created, so no live state is touched. Timestamps are computed here (not in
// bash) so it is portable across Linux and macOS.
//
// Usage:  node scripts/console_render_seed.mjs <bpHome> <stateDir>

import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const bpHome = process.argv[2];
const stateDir = process.argv[3];
if (!bpHome || !stateDir) {
  console.error("usage: node console_render_seed.mjs <bpHome> <stateDir>");
  process.exit(2);
}

const now = new Date();
const iso = (d) => d.toISOString().replace(/\.\d+Z$/, "Z");
const future = (hours) => iso(new Date(now.getTime() + hours * 3600 * 1000));
const fetchedAt = iso(now);

// config.json: the owner-set usage/pricing block plus an explicit stateDir so
// the console, the account store and the token store all agree on one location.
writeFileSync(
  join(bpHome, "config.json"),
  JSON.stringify(
    {
      stateDir,
      usage: { plan: "team", perSeatUsd: 150, perSeatFiveHourTokens: 50_000_000 },
    },
    null,
    2,
  ) + "\n",
);

// accounts.json: four Team premium seats, each with a cached five-hour reading
// (utilizations mirror a realistic fleet) and a reset time in the future so the
// window reads as live rather than reset-to-zero.
const accDir = join(stateDir, "claude-accounts");
mkdirSync(accDir, { recursive: true });
const seat = (number, alias, email, util, resetHours) => ({
  number,
  email,
  accountUuid: `uuid-${number}`,
  orgUuid: "org-1",
  orgName: "Probot Studio",
  alias,
  addedAt: fetchedAt,
  lastUsage: {
    usage: { fiveHour: { utilization: util, resetsAt: future(resetHours) } },
    fetchedAt,
  },
});
writeFileSync(
  join(accDir, "accounts.json"),
  JSON.stringify(
    {
      version: 1,
      active: 1,
      slots: [
        seat(1, "tuna", "tuna@probotstudio.com", 15, 1.2),
        seat(2, "azra", "azra@probotstudio.com", 1, 4.8),
        seat(3, "huseyin", "huseyin@probotstudio.com", 2, 3.6),
        seat(4, "cerci", "yigit@probotstudio.com", 3, 2.3),
      ],
    },
    null,
    2,
  ) + "\n",
);

// tokens/daily/<year>.jsonl: real per-model usage so the spend panel prices
// actual tokens (cache reads dominate, as in production) rather than estimating.
const tokDir = join(stateDir, "tokens", "daily");
mkdirSync(tokDir, { recursive: true });
const day = fetchedAt.slice(0, 10);
const year = day.slice(0, 4);
const rec = (agent, src, model, inTok, out, cacheWrite, cacheRead) =>
  JSON.stringify({ day, agent, src, model, reqs: 10, in: inTok, out, cache_write: cacheWrite, cache_read: cacheRead });
writeFileSync(
  join(tokDir, `${year}.jsonl`),
  [
    rec("blueprint", "claude", "claude-opus-4-8", 40_000_000, 3_000_000, 5_000_000, 120_000_000),
    rec("probot-egitim", "claude", "claude-sonnet-5", 20_000_000, 2_000_000, 2_000_000, 60_000_000),
    rec("akbank-main", "claude", "claude-haiku-5", 10_000_000, 1_000_000, 1_000_000, 30_000_000),
    // A non-Claude (Codex/GPT) row: its tokens count but are not priced at
    // Claude API rates, proving the "if it were API" figure stays Claude-only.
    rec("legacy", "codex", "gpt-6", 5_000_000, 500_000, 0, 0),
  ].join("\n") + "\n",
);

console.log(`SEED OK: config.json + 4 accounts + token store under ${stateDir}`);
