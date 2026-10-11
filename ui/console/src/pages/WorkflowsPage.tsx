// WorkflowsPage lists saved workflow definitions (GET /v1/workflows). A real
// fleet saves a workflow as many phase definitions that belong to one pipeline
// (e.g. sayfa-ders-1-plan .. sayfa-ders-9-yayin, or hack-gorev / hack-metin /
// hack-tarama). Shown flat, that reads as a jumble of unrelated rows. So we group
// definitions into families by their shared name stem and render each family as
// one coherent, collapsible workflow with its ordered phases inside — plus a
// search box to filter. "Duplicate" (POST /v1/workflows/{name}/clone) stays per
// phase so any single definition is reusable.

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import {
  Workflow as WorkflowIcon,
  Copy,
  ChevronRight,
  ChevronDown,
  Search,
  Layers,
} from "lucide-react";

import { workflowsApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import type { WorkflowSummary } from "../lib/types";
import { relativeTime } from "../lib/utils";
import { Button, Card, Input, PageState, Spinner } from "../components/ui";

// familyStem derives the shared pipeline name. "sayfa-ders-1-plan" -> "sayfa-ders"
// (everything before the first -<number> segment); a name with no numbered phase
// falls back to its first hyphen token ("hack-gorev" -> "hack"), so sibling
// definitions group even when they are not numbered.
function familyStem(name: string): string {
  const numbered = name.match(/^(.*?)-\d+(?:\b|-)/);
  if (numbered) return numbered[1];
  const dash = name.indexOf("-");
  return dash > 0 ? name.slice(0, dash) : name;
}

// phaseIndex pulls the leading phase number so phases order naturally (1..9) even
// when the saved names would sort lexically wrong (10 before 2).
function phaseIndex(name: string, stem: string): number {
  const rest = name.slice(stem.length);
  const m = rest.match(/\d+/);
  return m ? parseInt(m[0], 10) : Number.MAX_SAFE_INTEGER;
}

interface Family {
  stem: string;
  members: WorkflowSummary[];
  units: number;
  updatedAt?: string;
}

function groupFamilies(workflows: WorkflowSummary[]): Family[] {
  const byStem = new Map<string, WorkflowSummary[]>();
  for (const wf of workflows) {
    const stem = familyStem(wf.name);
    const list = byStem.get(stem) ?? [];
    list.push(wf);
    byStem.set(stem, list);
  }
  const families: Family[] = [];
  for (const [stem, members] of byStem) {
    members.sort((a, b) => {
      const pa = phaseIndex(a.name, stem);
      const pb = phaseIndex(b.name, stem);
      return pa !== pb ? pa - pb : a.name.localeCompare(b.name);
    });
    const units = members.reduce((s, m) => s + (m.units ?? 0), 0);
    const updatedAt = members
      .map((m) => m.updatedAt)
      .filter((v): v is string => Boolean(v))
      .sort()
      .at(-1);
    families.push({ stem, members, units, updatedAt });
  }
  families.sort((a, b) => a.stem.localeCompare(b.stem));
  return families;
}

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

  const [search, setSearch] = useState("");
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});

  const workflows = useMemo(() => listQuery.data ?? [], [listQuery.data]);
  const families = useMemo(() => {
    const q = search.trim().toLowerCase();
    const matched = q
      ? workflows.filter(
          (w) =>
            w.name.toLowerCase().includes(q) ||
            (w.description ?? "").toLowerCase().includes(q),
        )
      : workflows;
    return groupFamilies(matched);
  }, [workflows, search]);

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

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between gap-3 border-b border-border px-5 py-2">
        <div className="flex items-baseline gap-2">
          <h1 className="text-sm font-semibold">Workflows</h1>
          <span className="text-[11px] text-muted-foreground">
            {families.length} pipelines · {workflows.length} definitions
          </span>
        </div>
        <div className="flex items-center gap-2">
          {cloneMut.isError && (
            <span className="text-[11px] text-destructive">
              {(cloneMut.error as Error).message}
            </span>
          )}
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search workflows…"
              className="h-8 w-56 pl-7"
            />
          </div>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto p-5">
        {families.length === 0 ? (
          <PageState
            icon={WorkflowIcon}
            title={search ? "No workflows match your search." : "No saved workflows."}
          />
        ) : (
          <div className="flex flex-col gap-3">
            {families.map((fam) =>
              fam.members.length === 1 ? (
                <WorkflowRow
                  key={fam.stem}
                  wf={fam.members[0]}
                  busy={cloneMut.isPending}
                  onClone={(to) => cloneMut.mutate({ name: fam.members[0].name, to })}
                />
              ) : (
                <FamilyCard
                  key={fam.stem}
                  family={fam}
                  collapsed={collapsed[fam.stem] ?? false}
                  onToggle={() =>
                    setCollapsed((c) => ({ ...c, [fam.stem]: !(c[fam.stem] ?? false) }))
                  }
                  busy={cloneMut.isPending}
                  onClone={(name, to) => cloneMut.mutate({ name, to })}
                />
              ),
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function FamilyCard({
  family,
  collapsed,
  onToggle,
  busy,
  onClone,
}: {
  family: Family;
  collapsed: boolean;
  onToggle: () => void;
  busy: boolean;
  onClone: (name: string, to: string) => void;
}) {
  return (
    <Card className="overflow-hidden p-0">
      <button
        onClick={onToggle}
        className="flex w-full items-center gap-2 px-4 py-3 text-left hover:bg-accent/50"
      >
        {collapsed ? (
          <ChevronRight className="h-4 w-4 text-muted-foreground" />
        ) : (
          <ChevronDown className="h-4 w-4 text-muted-foreground" />
        )}
        <Layers className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-semibold">{family.stem}</span>
        <span className="text-[11px] text-muted-foreground">
          {family.members.length} phases · {family.units} units
        </span>
        {family.updatedAt && (
          <span className="ml-auto text-[11px] text-muted-foreground">
            updated {relativeTime(family.updatedAt)}
          </span>
        )}
      </button>
      {!collapsed && (
        <ol className="border-t border-border">
          {family.members.map((wf, i) => (
            <PhaseRow
              key={wf.name}
              index={i + 1}
              wf={wf}
              busy={busy}
              onClone={(to) => onClone(wf.name, to)}
            />
          ))}
        </ol>
      )}
    </Card>
  );
}

function PhaseRow({
  index,
  wf,
  busy,
  onClone,
}: {
  index: number;
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
    <li className="border-b border-border/60 last:border-b-0">
      <div className="flex items-center gap-3 px-4 py-2.5">
        <span className="w-5 shrink-0 text-center text-[11px] font-mono text-muted-foreground">
          {index}
        </span>
        <Link to={`/workflows/${encodeURIComponent(wf.name)}`} className="group min-w-0 flex-1">
          <span className="flex items-center gap-1 text-[13px] font-medium group-hover:underline">
            {wf.name}
            <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          </span>
          {wf.description && (
            <p className="mt-0.5 line-clamp-1 text-[12px] text-muted-foreground">
              {wf.description}
            </p>
          )}
        </Link>
        {wf.units !== undefined && (
          <span className="shrink-0 text-[11px] text-muted-foreground">{wf.units} units</span>
        )}
        <Button
          variant="outline"
          onClick={() => setCloning((v) => !v)}
          title="Duplicate workflow"
        >
          <Copy className="h-3.5 w-3.5" />
        </Button>
      </div>
      {cloning && (
        <div className="flex items-center gap-2 px-4 pb-3 pl-12">
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
    </li>
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
