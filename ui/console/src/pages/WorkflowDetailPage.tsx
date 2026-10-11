// WorkflowDetailPage shows one workflow (GET /v1/workflows/{name}): its units
// (the multi-agent graph, rendered as a dependency list) and its recent runs.

import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, Workflow as WorkflowIcon } from "lucide-react";

import { workflowsApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import type { WorkflowRun, WorkflowUnit } from "../lib/types";
import { relativeTime } from "../lib/utils";
import { Badge, Card, PageState, Spinner } from "../components/ui";

const RUN_STATUS_COLORS: Record<string, string> = {
  completed: "#10b981",
  succeeded: "#10b981",
  running: "#22d3ee",
  failed: "#ef4444",
  cancelled: "#6b7280",
};

export function WorkflowDetailPage() {
  const { name = "" } = useParams();
  const detailQuery = useQuery({
    queryKey: queryKeys.workflows.detail(name),
    queryFn: () => workflowsApi.get(name),
    enabled: !!name,
  });

  return (
    <div className="h-full overflow-auto">
      <div className="flex items-center gap-2 border-b border-border px-5 py-2">
        <Link
          to="/workflows"
          className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-3.5 w-3.5" /> Workflows
        </Link>
        <span className="text-muted-foreground">/</span>
        <h1 className="text-sm font-semibold">{name}</h1>
      </div>

      {detailQuery.isLoading ? (
        <div className="flex h-40 items-center justify-center">
          <Spinner />
        </div>
      ) : detailQuery.isError ? (
        <PageState
          icon={WorkflowIcon}
          title="Could not load workflow"
          detail={(detailQuery.error as Error).message}
        />
      ) : (
        <div className="space-y-4 p-5">
          {detailQuery.data?.description && (
            <p className="text-sm text-muted-foreground">{detailQuery.data.description}</p>
          )}
          <Units units={detailQuery.data?.units ?? []} />
          <Runs runs={detailQuery.data?.runs ?? []} />
        </div>
      )}
    </div>
  );
}

function Units({ units }: { units: WorkflowUnit[] }) {
  return (
    <Card className="p-4">
      <h2 className="mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        Units ({units.length})
      </h2>
      {units.length === 0 ? (
        <p className="text-sm text-muted-foreground">This workflow defines no units.</p>
      ) : (
        <ul className="divide-y divide-border">
          {units.map((unit) => (
            <li key={unit.name} className="flex flex-col gap-1 py-2.5">
              <div className="flex items-center gap-2">
                <span className="font-mono text-[13px] font-medium">{unit.name}</span>
                {unit.agent && (
                  <Badge className="text-muted-foreground">{unit.agent}</Badge>
                )}
                {unit.role && <span className="text-[11px] text-muted-foreground">{unit.role}</span>}
              </div>
              {unit.needs && unit.needs.length > 0 && (
                <span className="text-[11px] text-muted-foreground">
                  needs: {unit.needs.join(", ")}
                </span>
              )}
              {unit.prompt && (
                <p className="line-clamp-2 text-[12px] text-muted-foreground/80">{unit.prompt}</p>
              )}
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function Runs({ runs }: { runs: WorkflowRun[] }) {
  return (
    <Card className="p-4">
      <h2 className="mb-3 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        Recent runs ({runs.length})
      </h2>
      {runs.length === 0 ? (
        <p className="text-sm text-muted-foreground">No runs recorded yet.</p>
      ) : (
        <ul className="divide-y divide-border">
          {runs.map((run) => (
            <li key={run.id} className="flex items-center justify-between py-2 text-sm">
              <span className="flex items-center gap-2">
                <span
                  className="h-2 w-2 rounded-full"
                  style={{ backgroundColor: RUN_STATUS_COLORS[run.status ?? ""] ?? "#a3a3a3" }}
                />
                <span className="font-mono text-[12px]">{run.id}</span>
                {run.status && <span className="text-[11px] text-muted-foreground">{run.status}</span>}
              </span>
              <span className="text-[11px] text-muted-foreground tabular-nums">
                {run.startedAt ? relativeTime(run.startedAt) : ""}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
