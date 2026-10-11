// TasksPage shows bp tasks as a state board and a list, with create and
// state-edit. The board moves a task between states with native drag-and-drop
// (no dnd-kit, to keep the bundle lean). "Todo (mine)" filters to the identity
// the viewer records locally, since the console has no server-side "current
// identity" endpoint; it never guesses one.

import { useMemo, useState, type DragEvent, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ListTodo, LayoutGrid, Rows3, Plus } from "lucide-react";

import { tasksApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import { TASK_STATES, type Task, type TaskState } from "../lib/types";
import { TASK_STATE_COLORS, TASK_STATE_LABELS, cn, relativeTime } from "../lib/utils";
import { Button, Card, Input, PageState, Select, Spinner } from "../components/ui";

const ME_KEY = "bp-console-identity";

function loadMe(): string {
  try {
    return localStorage.getItem(ME_KEY) ?? "";
  } catch {
    return "";
  }
}

export function TasksPage() {
  const queryClient = useQueryClient();
  const [view, setView] = useState<"board" | "list">("board");
  const [me, setMe] = useState(loadMe);
  const [mineOnly, setMineOnly] = useState(false);
  const [stateFilter, setStateFilter] = useState<"" | TaskState>("");

  const owner = mineOnly ? me : "";
  const filter = { owner: owner || undefined, state: stateFilter || undefined };

  const tasksQuery = useQuery({
    queryKey: queryKeys.tasks.list(filter),
    queryFn: () => tasksApi.list(filter),
  });

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["tasks"] });

  const patchMut = useMutation({
    mutationFn: ({ id, state }: { id: string; state: TaskState }) =>
      tasksApi.patch(id, { state }),
    onSettled: invalidate,
  });
  const createMut = useMutation({
    mutationFn: tasksApi.create,
    onSettled: invalidate,
  });

  const tasks = tasksQuery.data ?? [];

  const persistMe = (value: string) => {
    setMe(value);
    try {
      localStorage.setItem(ME_KEY, value);
    } catch {
      // storage blocked; the value still applies this session
    }
  };

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-5 py-2">
        <h1 className="text-sm font-semibold">Tasks</h1>
        <div className="flex flex-wrap items-center gap-2">
          <Select
            value={stateFilter}
            onChange={(e) => setStateFilter(e.target.value as "" | TaskState)}
            aria-label="Filter by state"
          >
            <option value="">All states</option>
            {TASK_STATES.map((s) => (
              <option key={s} value={s}>
                {TASK_STATE_LABELS[s]}
              </option>
            ))}
          </Select>
          <Input
            value={me}
            onChange={(e) => persistMe(e.target.value)}
            placeholder="your agent name"
            className="w-36"
            aria-label="Your identity for the Todo view"
          />
          <Button
            variant={mineOnly ? "default" : "outline"}
            onClick={() => setMineOnly((v) => !v)}
            disabled={!me}
            title={me ? "Show only tasks you own" : "Set your agent name to use Todo"}
          >
            Todo (mine)
          </Button>
          <div className="flex overflow-hidden rounded-sm border border-border">
            <button
              onClick={() => setView("board")}
              className={cn(
                "flex items-center gap-1 px-2 py-1.5 text-xs",
                view === "board" ? "bg-accent" : "hover:bg-accent/60",
              )}
            >
              <LayoutGrid className="h-3.5 w-3.5" /> Board
            </button>
            <button
              onClick={() => setView("list")}
              className={cn(
                "flex items-center gap-1 border-l border-border px-2 py-1.5 text-xs",
                view === "list" ? "bg-accent" : "hover:bg-accent/60",
              )}
            >
              <Rows3 className="h-3.5 w-3.5" /> List
            </button>
          </div>
        </div>
      </div>

      <CreateTask
        defaultOwner={me}
        busy={createMut.isPending}
        onCreate={(task) => createMut.mutate(task)}
      />

      <div className="min-h-0 flex-1 overflow-auto p-4">
        {tasksQuery.isLoading ? (
          <div className="flex h-40 items-center justify-center">
            <Spinner />
          </div>
        ) : tasksQuery.isError ? (
          <PageState
            icon={ListTodo}
            title="Could not load tasks"
            detail={(tasksQuery.error as Error).message}
          />
        ) : tasks.length === 0 ? (
          <PageState icon={ListTodo} title="No tasks match this view." />
        ) : view === "board" ? (
          <Board tasks={tasks} onMove={(id, state) => patchMut.mutate({ id, state })} />
        ) : (
          <TaskList tasks={tasks} onMove={(id, state) => patchMut.mutate({ id, state })} />
        )}
      </div>
    </div>
  );
}

function CreateTask({
  defaultOwner,
  busy,
  onCreate,
}: {
  defaultOwner: string;
  busy: boolean;
  onCreate: (task: { title: string; owner?: string; state: TaskState }) => void;
}) {
  const [title, setTitle] = useState("");
  const [owner, setOwner] = useState("");
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = title.trim();
    if (!trimmed) return;
    onCreate({ title: trimmed, owner: (owner || defaultOwner) || undefined, state: "todo" });
    setTitle("");
    setOwner("");
  };
  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-2 border-b border-border px-5 py-2.5">
      <Input
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        placeholder="New task title"
        className="min-w-48 flex-1"
      />
      <Input
        value={owner}
        onChange={(e) => setOwner(e.target.value)}
        placeholder={defaultOwner ? `owner (${defaultOwner})` : "owner (optional)"}
        className="w-44"
      />
      <Button type="submit" disabled={busy || !title.trim()}>
        <Plus className="h-3.5 w-3.5" /> Add
      </Button>
    </form>
  );
}

function Board({
  tasks,
  onMove,
}: {
  tasks: Task[];
  onMove: (id: string, state: TaskState) => void;
}) {
  const byState = useMemo(() => {
    const map: Record<TaskState, Task[]> = {
      todo: [],
      in_progress: [],
      blocked: [],
      done: [],
      cancelled: [],
    };
    for (const task of tasks) {
      (map[task.state] ?? map.todo).push(task);
    }
    return map;
  }, [tasks]);

  const [over, setOver] = useState<TaskState | null>(null);

  const onDrop = (e: DragEvent, state: TaskState) => {
    e.preventDefault();
    setOver(null);
    const id = e.dataTransfer.getData("text/task-id");
    if (id) onMove(id, state);
  };

  return (
    <div className="flex gap-3 overflow-x-auto pb-2">
      {TASK_STATES.map((state) => (
        <div
          key={state}
          onDragOver={(e) => {
            e.preventDefault();
            setOver(state);
          }}
          onDragLeave={() => setOver((cur) => (cur === state ? null : cur))}
          onDrop={(e) => onDrop(e, state)}
          className={cn(
            "flex w-64 shrink-0 flex-col rounded-sm border border-border bg-background",
            over === state && "ring-2 ring-ring",
          )}
        >
          <div className="flex items-center justify-between border-b border-border px-3 py-2">
            <span className="flex items-center gap-1.5 text-xs font-medium">
              <span className="h-2 w-2 rounded-full" style={{ backgroundColor: TASK_STATE_COLORS[state] }} />
              {TASK_STATE_LABELS[state]}
            </span>
            <span className="text-[11px] text-muted-foreground tabular-nums">
              {byState[state].length}
            </span>
          </div>
          <div className="flex flex-col gap-2 p-2">
            {byState[state].map((task) => (
              <BoardCard key={task.id} task={task} />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function BoardCard({ task }: { task: Task }) {
  return (
    <Card
      draggable
      onDragStart={(e) => e.dataTransfer.setData("text/task-id", task.id)}
      className="cursor-grab p-2.5 active:cursor-grabbing"
    >
      <p className="text-[13px] font-medium leading-snug">{task.title}</p>
      <div className="mt-1.5 flex items-center justify-between text-[11px] text-muted-foreground">
        <span className="truncate">{task.owner || "unassigned"}</span>
        {task.updatedAt && <span className="tabular-nums">{relativeTime(task.updatedAt)}</span>}
      </div>
    </Card>
  );
}

function TaskList({
  tasks,
  onMove,
}: {
  tasks: Task[];
  onMove: (id: string, state: TaskState) => void;
}) {
  return (
    <div className="overflow-hidden rounded-sm border border-border">
      <table className="w-full text-sm">
        <thead className="bg-muted/50 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
          <tr>
            <th className="px-3 py-2 font-medium">Task</th>
            <th className="px-3 py-2 font-medium">Owner</th>
            <th className="px-3 py-2 font-medium">State</th>
            <th className="px-3 py-2 font-medium">Updated</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {tasks.map((task) => (
            <tr key={task.id} className="hover:bg-accent/40">
              <td className="px-3 py-2">
                <span className="font-medium">{task.title}</span>
                {task.derivedStatus && task.derivedStatus !== task.state && (
                  <span className="ml-2 text-[11px] text-muted-foreground">
                    (runtime: {task.derivedStatus})
                  </span>
                )}
              </td>
              <td className="px-3 py-2 text-muted-foreground">{task.owner || "—"}</td>
              <td className="px-3 py-2">
                <Select
                  value={task.state}
                  onChange={(e) => onMove(task.id, e.target.value as TaskState)}
                  aria-label={`State of ${task.title}`}
                >
                  {TASK_STATES.map((s) => (
                    <option key={s} value={s}>
                      {TASK_STATE_LABELS[s]}
                    </option>
                  ))}
                </Select>
              </td>
              <td className="px-3 py-2 text-muted-foreground tabular-nums">
                {task.updatedAt ? relativeTime(task.updatedAt) : "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
