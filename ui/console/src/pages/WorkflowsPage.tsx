// WorkflowsPage lists saved workflow definitions (GET /v1/workflows) and offers
// "Duplicate" (POST /v1/workflows/{name}/clone) so a workflow is reusable.

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Workflow as WorkflowIcon, Copy, ChevronRight } from "lucide-react";

import { workflowsApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import type { WorkflowSummary } from "../lib/types";
import { relativeTime } from "../lib/utils";
import { Button, Card, Input, PageState, Spinner } from "../components/ui";

export function WorkflowsPage() {
  const queryClient = useQueryClient();
  const listQuery = useQuery({
    queryKey: queryKeys.workflows.list(),
    queryFn: workflowsApi.list,
  });

  const cloneMut = useMutation({
    mutationFn: ({ name, to }: { name: string; to: string }) =>
      workflowsApi.clone(name, to),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: queryKeys.workflows.list() }),
  });

  if (listQuery.isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (listQuery.isError) {
    return (
      <PageState
        icon={WorkflowIcon}
        title="Could not load workflows"
        detail={(listQuery.error as Error).message}
      />
    );
  }

  const workflows = listQuery.data ?? [];

  return (
    <div className="h-full overflow-auto">
      <div className="flex items-center justify-between border-b border-border px-5 py-2">
        <h1 className="text-sm font-semibold">Workflows</h1>
        {cloneMut.isError && (
          <span className="text-[11px] text-destructive">
            {(cloneMut.error as Error).message}
          </span>
        )}
      </div>

      <div className="p-5">
        {workflows.length === 0 ? (
          <PageState icon={WorkflowIcon} title="No saved workflows." />
        ) : (
          <div className="grid gap-3 md:grid-cols-2">
            {workflows.map((wf) => (
              <WorkflowRow
                key={wf.name}
                wf={wf}
                busy={cloneMut.isPending}
                onClone={(to) => cloneMut.mutate({ name: wf.name, to })}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function WorkflowRow({
  wf,
  busy,
  onClone,
}: {
  wf: WorkflowSummary;
  busy: boolean;
  onClone: (to: string) => void;
}) {
  const [cloning, setCloning] = useState(false);
  const [to, setTo] = useState("");
  const submit = () => {
    const name = to.trim();
    if (!name) return;
    onClone(name);
    setCloning(false);
    setTo("");
  };
  return (
    <Card className="p-4">
      <div className="flex items-start justify-between gap-2">
        <Link to={`/workflows/${encodeURIComponent(wf.name)}`} className="group min-w-0">
          <span className="flex items-center gap-1 text-sm font-semibold group-hover:underline">
            {wf.name}
            <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          </span>
          {wf.description && (
            <p className="mt-0.5 line-clamp-2 text-[12px] text-muted-foreground">{wf.description}</p>
          )}
        </Link>
        <Button variant="outline" onClick={() => setCloning((v) => !v)} title="Duplicate workflow">
          <Copy className="h-3.5 w-3.5" /> Duplicate
        </Button>
      </div>

      <div className="mt-2 flex items-center gap-3 text-[11px] text-muted-foreground">
        {wf.units !== undefined && <span>{wf.units} units</span>}
        {wf.updatedAt && <span>updated {relativeTime(wf.updatedAt)}</span>}
      </div>

      {cloning && (
        <div className="mt-3 flex items-center gap-2">
          <Input
            autoFocus
            value={to}
            onChange={(e) => setTo(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && submit()}
            placeholder={`${wf.name}-copy`}
            className="flex-1"
          />
          <Button onClick={submit} disabled={busy || !to.trim()}>
            Save copy
          </Button>
        </div>
      )}
    </Card>
  );
}
