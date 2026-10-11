// types.ts mirrors the bp fleet console API contract (docs/fleet-console.md).
// The shapes here are the minimum each screen reads; fields the backend may omit
// are optional so an older or partial response never crashes a view.

// ── Fleet / hierarchy (WS1) ────────────────────────────────────────────────

export type AgentStatus =
  | "idle"
  | "working"
  | "blocked"
  | "dead"
  | "unknown"
  | string;

export interface FleetAgent {
  name: string;
  parent: string;
  role?: string;
  host?: string;
  runtime?: string;
  status: AgentStatus;
  statusReason?: string;
  lastTurnAt?: string;
  folder?: string;
  open: boolean;
}

export interface FleetTree {
  agents: FleetAgent[];
}

export interface LayoutPos {
  x: number;
  y: number;
}

export interface FleetLayout {
  positions: Record<string, LayoutPos>;
}

export interface ReparentResponse {
  agent: string;
  parent: string;
  previous: string;
}

// ── Tasks (WS2) ────────────────────────────────────────────────────────────

export type TaskState =
  | "todo"
  | "in_progress"
  | "blocked"
  | "done"
  | "cancelled";

export const TASK_STATES: TaskState[] = [
  "todo",
  "in_progress",
  "blocked",
  "done",
  "cancelled",
];

export interface Task {
  id: string;
  title: string;
  owner: string;
  parent: string;
  state: TaskState;
  body?: string;
  acceptance?: string;
  createdAt?: string;
  updatedAt?: string;
  derivedStatus?: string;
}

export interface TaskDetail {
  task: Task;
  children: Task[];
}

export interface NewTask {
  title: string;
  owner?: string;
  parent?: string;
  state?: TaskState;
  body?: string;
  acceptance?: string;
}

export interface TaskPatch {
  title?: string;
  owner?: string;
  parent?: string;
  state?: TaskState;
  body?: string;
  acceptance?: string;
}

// ── Usage (WS3) ────────────────────────────────────────────────────────────

export interface UsageAccount {
  name: string;
  slot?: number;
  percentOfLimit: number;
  limit?: number;
  estTokens?: number;
  fiveHourResetAt?: string;
  window?: string;
  active?: boolean;
}

export interface UsageDay {
  date: string;
  percent: number;
  estTokens?: number;
}

export interface UsageAccounts {
  asOf?: string;
  accounts: UsageAccount[];
  recentDays?: UsageDay[];
}

// ── Workflows (WS4) ──────────────────────────────────────────────────────────

export interface WorkflowSummary {
  name: string;
  description?: string;
  units?: number;
  updatedAt?: string;
}

export interface WorkflowUnit {
  name: string;
  agent?: string;
  role?: string;
  needs?: string[];
  prompt?: string;
}

export interface WorkflowRun {
  id: string;
  status?: string;
  startedAt?: string;
  finishedAt?: string;
}

export interface WorkflowDetail {
  name: string;
  description?: string;
  definition?: unknown;
  units: WorkflowUnit[];
  runs: WorkflowRun[];
}
