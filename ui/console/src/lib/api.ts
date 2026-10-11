// api.ts is the single place the console talks to bp's /v1 API. Every read and
// write goes through apiFetch, which attaches the loopback Bearer token the
// daemon injected into the page (window.__BP_TOKEN__) and maps bp's
// {error:{code,message}} shape to a thrown ApiError. Requests are same-origin:
// the SPA is served by the daemon on the same loopback origin as the API.

import type {
  FleetTree,
  FleetLayout,
  LayoutPos,
  ReparentResponse,
  Task,
  TaskDetail,
  NewTask,
  TaskPatch,
  UsageAccounts,
  WorkflowSummary,
  WorkflowDetail,
} from "./types";

// The literal placeholder means the daemon did not inject a token (e.g. under
// `npm run dev`); treat it as absent and issue tokenless same-origin requests.
const TOKEN_PLACEHOLDER = "__BP_CONSOLE_TOKEN__";

function bearerToken(): string | undefined {
  const token = typeof window !== "undefined" ? window.__BP_TOKEN__ : undefined;
  if (!token || token === TOKEN_PLACEHOLDER) return undefined;
  return token;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

interface ApiErrorBody {
  error?: { code?: string; message?: string };
  message?: string;
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = new Headers(init?.headers);
  const token = bearerToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (init?.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  headers.set("Accept", "application/json");

  let res: Response;
  try {
    res = await fetch(path, { ...init, headers });
  } catch (cause) {
    throw new ApiError(0, "NETWORK", `cannot reach bp: ${String(cause)}`);
  }

  const text = await res.text();
  const data = text ? safeParse(text) : undefined;

  if (!res.ok) {
    const body = (data ?? {}) as ApiErrorBody;
    const code = body.error?.code ?? "ERROR";
    const message =
      body.error?.message ?? body.message ?? res.statusText ?? "request failed";
    throw new ApiError(res.status, code, message);
  }
  return data as T;
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

// unwrap tolerates both a bare array and an enveloped {<key>: array} response,
// since the sibling workstreams may wrap their lists. It never guesses contents.
function unwrap<T>(data: unknown, key: string): T[] {
  if (Array.isArray(data)) return data as T[];
  if (data && typeof data === "object") {
    const value = (data as Record<string, unknown>)[key];
    if (Array.isArray(value)) return value as T[];
  }
  return [];
}

function qs(params: Record<string, string | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value) search.set(key, value);
  }
  const s = search.toString();
  return s ? `?${s}` : "";
}

// ── Fleet / hierarchy ──────────────────────────────────────────────────────

export const fleetApi = {
  tree: () => apiFetch<FleetTree>("/v1/fleet/tree"),
  reparent: (agent: string, parent: string) =>
    apiFetch<ReparentResponse>("/v1/fleet/reparent", {
      method: "POST",
      body: JSON.stringify({ agent, parent }),
    }),
  layout: () => apiFetch<FleetLayout>("/v1/fleet/layout"),
  saveLayout: (positions: Record<string, LayoutPos>) =>
    apiFetch<FleetLayout>("/v1/fleet/layout", {
      method: "POST",
      body: JSON.stringify({ positions }),
    }),
};

// ── Tasks ──────────────────────────────────────────────────────────────────

export const tasksApi = {
  list: (filter: { owner?: string; state?: string; parent?: string } = {}) =>
    apiFetch<unknown>(`/v1/tasks${qs(filter)}`).then((d) =>
      unwrap<Task>(d, "tasks"),
    ),
  get: (id: string) => apiFetch<TaskDetail>(`/v1/tasks/${encodeURIComponent(id)}`),
  create: (task: NewTask) =>
    apiFetch<Task>("/v1/tasks", {
      method: "POST",
      body: JSON.stringify(task),
    }),
  patch: (id: string, patch: TaskPatch) =>
    apiFetch<Task>(`/v1/tasks/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    }),
};

// ── Usage ──────────────────────────────────────────────────────────────────

export const usageApi = {
  accounts: () => apiFetch<UsageAccounts>("/v1/usage/accounts"),
};

// ── Workflows ──────────────────────────────────────────────────────────────

export const workflowsApi = {
  list: () =>
    apiFetch<unknown>("/v1/workflows").then((d) =>
      unwrap<WorkflowSummary>(d, "workflows"),
    ),
  get: (name: string) =>
    apiFetch<WorkflowDetail>(`/v1/workflows/${encodeURIComponent(name)}`),
  clone: (name: string, to: string) =>
    apiFetch<WorkflowSummary>(
      `/v1/workflows/${encodeURIComponent(name)}/clone`,
      { method: "POST", body: JSON.stringify({ to }) },
    ),
};
