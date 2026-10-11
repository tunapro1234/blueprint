import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

import type { AgentStatus, TaskPriority, TaskState } from "./types";

// cn merges Tailwind classes, with later classes winning conflicts.
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

// ── Status colors ────────────────────────────────────────────────────────────
// Explicit hex, matching the Paperclip design study (section 4). bp's runtime
// states map onto the study's palette; "unknown" is never recolored to look idle.

export const AGENT_STATUS_COLORS: Record<string, string> = {
  working: "#22d3ee", // cyan — actively running a turn
  active: "#4ade80",
  idle: "#4ade80", // green — alive and waiting
  blocked: "#f87171", // red — needs attention
  dead: "#6b7280", // grey — not running
  unknown: "#a3a3a3", // grey — honestly unknown, never a guessed idle
};

export function agentStatusColor(status: AgentStatus): string {
  return AGENT_STATUS_COLORS[status] ?? AGENT_STATUS_COLORS.unknown;
}

export const TASK_STATE_COLORS: Record<TaskState, string> = {
  todo: "#3b82f6",
  in_progress: "#8b5cf6",
  blocked: "#ef4444",
  done: "#10b981",
  cancelled: "#6b7280",
};

export const TASK_STATE_LABELS: Record<TaskState, string> = {
  todo: "Todo",
  in_progress: "In Progress",
  blocked: "Blocked",
  done: "Done",
  cancelled: "Cancelled",
};

// Priority palette climbs from a calm grey-blue (low) to red (urgent), the same
// visual ladder Asana/Notion use so the colour reads as urgency at a glance.
export const TASK_PRIORITY_COLORS: Record<TaskPriority, string> = {
  low: "#6b7280",
  medium: "#3b82f6",
  high: "#f59e0b",
  urgent: "#ef4444",
};

export const TASK_PRIORITY_LABELS: Record<TaskPriority, string> = {
  low: "Low",
  medium: "Medium",
  high: "High",
  urgent: "Urgent",
};

// ── Due dates ────────────────────────────────────────────────────────────────

// formatDue renders a YYYY-MM-DD due date compactly ("Mar 4"), or "" for an unset
// date. It parses as local noon so the day label never slips across a timezone.
export function formatDue(due: string | undefined): string {
  if (!due) return "";
  const d = new Date(`${due}T12:00:00`);
  if (Number.isNaN(d.getTime())) return due;
  return d.toLocaleDateString("en-US", { month: "short", day: "numeric" });
}

// dueState classifies a due date relative to today so a card can colour it:
// "overdue" (before today), "today", "soon" (within 2 days), or "" otherwise.
export function dueState(
  due: string | undefined,
  today: string,
): "overdue" | "today" | "soon" | "" {
  if (!due) return "";
  if (due < today) return "overdue";
  if (due === today) return "today";
  const d = new Date(`${due}T12:00:00`).getTime();
  const t = new Date(`${today}T12:00:00`).getTime();
  if (!Number.isNaN(d) && !Number.isNaN(t) && d - t <= 2 * 86_400_000) return "soon";
  return "";
}

// todayISO is today's calendar date (local) in YYYY-MM-DD, for due comparisons.
export function todayISO(): string {
  const now = new Date();
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, "0");
  const d = String(now.getDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

// ── Formatting ───────────────────────────────────────────────────────────────

// formatTokens renders a token count compactly (1.2M, 340k). Returns "—" for an
// unknown count so the UI never prints a guessed zero.
export function formatTokens(n: number | undefined): string {
  if (n === undefined || n === null || Number.isNaN(n)) return "—";
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}k`;
  return String(n);
}

export function formatUSD(n: number | undefined): string {
  if (n === undefined || n === null || Number.isNaN(n)) return "—";
  if (Math.abs(n) >= 1000) return `$${Math.round(n).toLocaleString("en-US")}`;
  return `$${n.toFixed(2)}`;
}

// formatPercent renders a 0..1 ratio as a whole-number percent.
export function formatPercent(ratio: number | undefined): string {
  if (ratio === undefined || ratio === null || Number.isNaN(ratio)) return "—";
  return `${Math.round(ratio * 100)}%`;
}

// usageBarColor follows the study's budget-bar thresholds: green < 70%, yellow
// 70–90%, red > 90%.
export function usageBarColor(ratioOfLimit: number): string {
  if (ratioOfLimit >= 0.9) return "#ef4444";
  if (ratioOfLimit >= 0.7) return "#eab308";
  return "#22c55e";
}

// relativeTime renders an RFC3339 timestamp as a short "2h ago" style string.
export function relativeTime(iso: string | undefined): string {
  if (!iso) return "";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  const diffMs = Date.now() - then;
  const abs = Math.abs(diffMs);
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  let value: number;
  let unit: string;
  if (abs < hour) {
    value = Math.round(abs / minute);
    unit = "m";
  } else if (abs < day) {
    value = Math.round(abs / hour);
    unit = "h";
  } else {
    value = Math.round(abs / day);
    unit = "d";
  }
  const suffix = diffMs >= 0 ? "ago" : "from now";
  return `${value}${unit} ${suffix}`;
}

// resetTime renders an RFC3339 reset timestamp as a local wall-clock time.
export function resetTime(iso: string | undefined): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
