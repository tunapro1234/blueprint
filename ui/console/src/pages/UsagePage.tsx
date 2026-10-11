// UsagePage is a Costs-style view of account utilization (GET /v1/usage/accounts):
// one budget bar per account (green <70% / yellow 70-90% / red >90%), the token
// count that percentage corresponds to — labelled as an estimate, never a fact —
// the window and reset time, and a hand-rolled recent-days bar chart (flexbox,
// no chart library).

import { useQuery } from "@tanstack/react-query";
import { Gauge } from "lucide-react";

import { usageApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import type {
  UsageAccount,
  UsageDay,
  UsageSpend,
  UsageSubscription,
  UsageValue,
} from "../lib/types";
import {
  formatPercent,
  formatTokens,
  formatUSD,
  relativeTime,
  resetTime,
  usageBarColor,
} from "../lib/utils";
import { Card, PageState, Spinner } from "../components/ui";

export function UsagePage() {
  const usageQuery = useQuery({
    queryKey: queryKeys.usage.accounts(),
    queryFn: usageApi.accounts,
    refetchInterval: 60_000,
  });

  if (usageQuery.isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (usageQuery.isError) {
    return (
      <PageState
        icon={Gauge}
        title="Could not load usage"
        detail={(usageQuery.error as Error).message}
      />
    );
  }

  const data = usageQuery.data;
  const accounts = data?.accounts ?? [];
  const recentDays = data?.recentDays ?? [];
  const subscription = data?.subscription ?? undefined;
  const spend = data?.spend ?? undefined;
  const value = data?.value ?? undefined;

  return (
    <div className="h-full overflow-auto">
      <div className="flex items-center justify-between border-b border-border px-5 py-2">
        <h1 className="text-sm font-semibold">Usage</h1>
        <span className="text-[11px] text-muted-foreground">
          Token counts are estimates · {data?.asOf ? `as of ${relativeTime(data.asOf)}` : "live"}
        </span>
      </div>

      <div className="space-y-4 p-5">
        {(subscription || spend || value) && (
          <ValueStrip subscription={subscription} spend={spend} value={value} />
        )}

        {accounts.length === 0 ? (
          <PageState icon={Gauge} title="No accounts reported." />
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {accounts.map((account) => (
              <AccountCard key={`${account.name}-${account.slot ?? ""}`} account={account} />
            ))}
          </div>
        )}

        {spend && spend.byModel && spend.byModel.length > 0 && <SpendBreakdown spend={spend} />}

        {recentDays.length > 0 && <RecentDays days={recentDays} />}
      </div>
    </div>
  );
}

// ValueStrip is the headline: the real subscription cost (seats x per-seat
// price), what the recent token burn would have cost at Claude API prices
// ("if it were API"), and the value ratio between them. Every tile renders only
// when its figure is actually known, so an unconfigured install shows fewer
// tiles rather than a guessed cost.
function ValueStrip({
  subscription,
  spend,
  value,
}: {
  subscription?: UsageSubscription;
  spend?: UsageSpend;
  value?: UsageValue;
}) {
  return (
    <Card className="p-4">
      <div className="grid gap-4 sm:grid-cols-3">
        {subscription && (
          <Tile
            label="Subscription"
            value={`${formatUSD(subscription.monthlyUsd)}/mo`}
            detail={`${subscription.seats} ${subscription.plan ? subscription.plan + " " : ""}seat${
              subscription.seats === 1 ? "" : "s"
            } · ${formatUSD(subscription.perSeatUsd)}/seat`}
          />
        )}
        {spend && (
          <Tile
            label={`If it were API · last ${spend.window}`}
            value={formatUSD(spend.apiEquivalentUsd)}
            detail={`${formatTokens(spend.totalTokens)} tokens burned`}
          />
        )}
        {value && (
          <Tile
            label="Value ratio"
            value={`${value.ratio.toLocaleString("en-US", { maximumFractionDigits: 1 })}×`}
            detail={`${formatUSD(value.apiEquivalentUsd)} API / ${formatUSD(value.subscriptionUsd)} plan`}
          />
        )}
      </div>
    </Card>
  );
}

function Tile({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div>
      <div className="text-[11px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 text-xl font-semibold tabular-nums">{value}</div>
      {detail && <div className="mt-0.5 text-[11px] text-muted-foreground">{detail}</div>}
    </div>
  );
}

// SpendBreakdown shows the API-equivalent cost per model family over the recent
// window. These are real recorded tokens priced at API rates, not estimates.
function SpendBreakdown({ spend }: { spend: UsageSpend }) {
  const max = Math.max(...spend.byModel!.map((m) => m.apiEquivalentUsd), 0.0001);
  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          API-equivalent spend by model
        </h2>
        <span className="text-[11px] text-muted-foreground">last {spend.window} · real tokens</span>
      </div>
      <div className="space-y-2">
        {spend.byModel!.map((m) => (
          <div key={m.model} className="flex items-center gap-3">
            <span className="w-20 shrink-0 text-xs font-medium">{m.model}</span>
            <div className="h-2 flex-1 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-[#8b5cf6]"
                style={{ width: `${(m.apiEquivalentUsd / max) * 100}%` }}
              />
            </div>
            <span className="w-28 shrink-0 text-right text-xs tabular-nums">
              {formatUSD(m.apiEquivalentUsd)}
              <span className="ml-1 text-[10px] text-muted-foreground">{formatTokens(m.tokens)}</span>
            </span>
          </div>
        ))}
      </div>
    </Card>
  );
}

function AccountCard({ account }: { account: UsageAccount }) {
  // percentOfLimit is the account's utilization of its own limit (0..1), so the
  // bar fill and the headline number are the same value and the green/yellow/red
  // thresholds read directly off it. `limit` is shown separately as context.
  const fill = clamp01(account.percentOfLimit);
  const limit = account.limit;
  return (
    <Card className="p-4">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold">{account.name}</span>
          {account.slot !== undefined && (
            <span className="text-[11px] text-muted-foreground">slot {account.slot}</span>
          )}
          {account.active && (
            <span className="rounded-full bg-[#22c55e]/15 px-1.5 py-0.5 text-[10px] font-medium text-[#16a34a]">
              active
            </span>
          )}
        </div>
        <span className="text-sm font-semibold tabular-nums">{formatPercent(account.percentOfLimit)}</span>
      </div>

      <div className="mt-3 h-2 w-full overflow-hidden rounded-full bg-muted">
        <div
          className="h-full rounded-full"
          style={{ width: `${fill * 100}%`, backgroundColor: usageBarColor(fill) }}
        />
      </div>

      <div className="mt-3 grid grid-cols-2 gap-y-1 text-[11px] text-muted-foreground">
        <span>Est. tokens</span>
        <span className="text-right tabular-nums text-foreground">
          {formatTokens(account.estTokens)}
          <span className="ml-1 text-muted-foreground">(est.)</span>
        </span>
        {limit !== undefined && (
          <>
            <span>Limit</span>
            <span className="text-right tabular-nums text-foreground">{formatPercent(limit)}</span>
          </>
        )}
        {account.window && (
          <>
            <span>Window</span>
            <span className="text-right text-foreground">{account.window}</span>
          </>
        )}
        {account.fiveHourResetAt && (
          <>
            <span>Resets</span>
            <span className="text-right text-foreground">
              {resetTime(account.fiveHourResetAt)}
              <span className="ml-1 text-muted-foreground">({relativeTime(account.fiveHourResetAt)})</span>
            </span>
          </>
        )}
      </div>
    </Card>
  );
}

function RecentDays({ days }: { days: UsageDay[] }) {
  const max = Math.max(...days.map((d) => d.percent), 0.0001);
  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Recent days
        </h2>
        <span className="text-[11px] text-muted-foreground">percent of limit</span>
      </div>
      <div className="flex h-32 items-end gap-1.5">
        {days.map((day) => (
          <div key={day.date} className="flex flex-1 flex-col items-center gap-1">
            <div
              className="w-full rounded-t-sm"
              style={{
                height: `${Math.max((day.percent / max) * 100, 2)}%`,
                backgroundColor: usageBarColor(clamp01(day.percent)),
              }}
              title={`${day.date}: ${formatPercent(day.percent)}${
                day.estTokens !== undefined ? ` · ~${formatTokens(day.estTokens)} tokens (est.)` : ""
              }`}
            />
            <span className="text-[9px] text-muted-foreground tabular-nums">
              {day.date.slice(5)}
            </span>
          </div>
        ))}
      </div>
    </Card>
  );
}

function clamp01(v: number | undefined): number {
  if (v === undefined || Number.isNaN(v)) return 0;
  if (v < 0) return 0;
  if (v > 1) return 1;
  return v;
}
