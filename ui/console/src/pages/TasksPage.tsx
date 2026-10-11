// TasksPage is the fleet console's task workspace: an Asana/Notion-style Kanban
// board plus a management list, over the same bp task store (docs/fleet-console.md,
// WS2). The board moves a task between states with native drag-and-drop (no
// dnd-kit, to keep the bundle lean) and adds tasks inline per column; clicking a
// card opens a detail drawer that edits every field, including priority, due date,
// assignee, parent, notes and acceptance, and can delete the task. The list view
// is the management surface: filter, group, sort, and edit state/priority inline.
//
// "Mine" filters to the identity the viewer records locally (ME_KEY), since the
// console has no server-side "current identity" endpoint; it never guesses one.

import {
  useEffect,
  useMemo,
  useState,
  type DragEvent,
  type FormEvent,
  type ReactNode,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Calendar,
  ChevronDown,
  LayoutGrid,
  ListTodo,
  Plus,
  Rows3,
  Search,
  Trash2,
  User,
  X,
} from "lucide-react";

import { tasksApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import {
  TASK_PRIORITIES,
  TASK_STATES,
  type NewTask,
  type Task,
  type TaskPatch,
  type TaskPriority,
  type TaskState,
} from "../lib/types";
import {
  TASK_PRIORITY_COLORS,
  TASK_PRIORITY_LABELS,
  TASK_STATE_COLORS,
  TASK_STATE_LABELS,
  cn,
  dueState,
  formatDue,
  relativeTime,
  todayISO,
} from "../lib/utils";
import {
  Button,
  Card,
  Input,
  PageState,
  Select,
  Spinner,
  Textarea,
} from "../components/ui";

const ME_KEY = "bp-console-identity";

function loadMe(): string {
  try {
    return localStorage.getItem(ME_KEY) ?? "";
  } catch {
    return "";
  }
}

// priorityRank orders a column so the loudest work floats up: urgent first, an
// unset priority last. It is a pure sort key, never shown.
const PRIORITY_RANK: Record<string, number> = {
  urgent: 0,
  high: 1,
  medium: 2,
  low: 3,
  "": 4,
};

function sortForColumn(a: Task, b: Task): number {
  const pr = PRIORITY_RANK[a.priority ?? ""] - PRIORITY_RANK[b.priority ?? ""];
  if (pr !== 0) return pr;
  // Then by due date: a dated task is more urgent than an undated one.
  const ad = a.due || "9999-99-99";
  const bd = b.due || "9999-99-99";
  if (ad !== bd) return ad < bd ? -1 : 1;
  return (a.createdAt ?? "") < (b.createdAt ?? "") ? -1 : 1;
}

export function TasksPage() {
  const queryClient = useQueryClient();
  const [view, setView] = useState<"board" | "list">("board");
  const [me, setMe] = useState(loadMe);
  const [mineOnly, setMineOnly] = useState(false);
  const [search, setSearch] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const owner = mineOnly ? me : "";
  const filter = { owner: owner || undefined };

  const tasksQuery = useQuery({
    queryKey: queryKeys.tasks.list(filter),
    queryFn: () => tasksApi.list(filter),
  });

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["tasks"] });

  const patchMut = useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: TaskPatch }) =>
      tasksApi.patch(id, patch),
    onSettled: invalidate,
  });
  const createMut = useMutation({
    mutationFn: (task: NewTask) => tasksApi.create(task),
    onSettled: invalidate,
  });
  const removeMut = useMutation({
    mutationFn: (id: string) => tasksApi.remove(id),
    onSettled: invalidate,
  });

  const persistMe = (value: string) => {
    setMe(value);
    try {
      localStorage.setItem(ME_KEY, value);
    } catch {
      // storage blocked; the value still applies this session
    }
  };

  const allTasks = tasksQuery.data ?? [];
  const today = todayISO();

  const tasks = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return allTasks;
    return allTasks.filter(
      (t) =>
        t.title.toLowerCase().includes(q) ||
        (t.owner ?? "").toLowerCase().includes(q) ||
        (t.body ?? "").toLowerCase().includes(q),
    );
  }, [allTasks, search]);

  const selected = useMemo(
    () => allTasks.find((t) => t.id === selectedId) ?? null,
    [allTasks, selectedId],
  );
  const children = useMemo(
    () => (selectedId ? allTasks.filter((t) => t.parent === selectedId) : []),
    [allTasks, selectedId],
  );

  // A selected task that disappears (deleted, filtered out of the owner query)
  // must not leave a stuck drawer.
  useEffect(() => {
    if (selectedId && !selected) setSelectedId(null);
  }, [selectedId, selected]);

  const move = (id: string, state: TaskState) =>
    patchMut.mutate({ id, patch: { state } });

  return (
    <div className="relative flex h-full flex-col">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-5 py-2">
        <div className="flex items-center gap-2">
          <h1 className="text-sm font-semibold">Tasks</h1>
          <span className="text-[11px] text-muted-foreground tabular-nums">
            {allTasks.length}
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search tasks"
              className="w-44 pl-7"
              aria-label="Search tasks"
            />
          </div>
          <Input
            value={me}
            onChange={(e) => persistMe(e.target.value)}
            placeholder="your agent name"
            className="w-36"
            aria-label="Your identity for the mine filter"
          />
          <Button
            variant={mineOnly ? "default" : "outline"}
            onClick={() => setMineOnly((v) => !v)}
            disabled={!me}
            title={me ? "Show only tasks you own" : "Set your agent name to use Mine"}
          >
            Mine
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
        ) : view === "board" ? (
          <Board
            tasks={tasks}
            today={today}
            onMove={move}
            onOpen={setSelectedId}
            onQuickAdd={(title, state) =>
              createMut.mutate({
                title,
                state,
                owner: (mineOnly && me) || undefined,
              })
            }
            adding={createMut.isPending}
          />
        ) : tasks.length === 0 ? (
          <PageState icon={ListTodo} title="No tasks match this view." />
        ) : (
          <TaskList
            tasks={tasks}
            today={today}
            onOpen={setSelectedId}
            onPatch={(id, patch) => patchMut.mutate({ id, patch })}
          />
        )}
      </div>

      {selected && (
        <TaskDrawer
          task={selected}
          children={children}
          today={today}
          saving={patchMut.isPending}
          deleting={removeMut.isPending}
          onClose={() => setSelectedId(null)}
          onSave={(patch) => patchMut.mutate({ id: selected.id, patch })}
          onDelete={() => {
            removeMut.mutate(selected.id);
            setSelectedId(null);
          }}
          onOpen={setSelectedId}
        />
      )}
    </div>
  );
}

// ── Board ────────────────────────────────────────────────────────────────────

function Board({
  tasks,
  today,
  onMove,
  onOpen,
  onQuickAdd,
  adding,
}: {
  tasks: Task[];
  today: string;
  onMove: (id: string, state: TaskState) => void;
  onOpen: (id: string) => void;
  onQuickAdd: (title: string, state: TaskState) => void;
  adding: boolean;
}) {
  const byState = useMemo(() => {
    const map: Record<TaskState, Task[]> = {
      todo: [],
      in_progress: [],
      blocked: [],
      done: [],
      cancelled: [],
    };
    for (const task of tasks) (map[task.state] ?? map.todo).push(task);
    for (const s of TASK_STATES) map[s].sort(sortForColumn);
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
    <div className="flex h-full gap-3 overflow-x-auto pb-2">
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
            "flex w-72 shrink-0 flex-col rounded-md border border-border bg-muted/30",
            over === state && "ring-2 ring-ring",
          )}
        >
          <div className="flex items-center justify-between px-3 py-2.5">
            <span className="flex items-center gap-1.5 text-xs font-semibold">
              <span
                className="h-2 w-2 rounded-full"
                style={{ backgroundColor: TASK_STATE_COLORS[state] }}
              />
              {TASK_STATE_LABELS[state]}
              <span className="text-[11px] font-normal text-muted-foreground tabular-nums">
                {byState[state].length}
              </span>
            </span>
          </div>
          <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-2 pb-2">
            {byState[state].map((task) => (
              <BoardCard
                key={task.id}
                task={task}
                today={today}
                onOpen={onOpen}
              />
            ))}
            <QuickAdd
              state={state}
              busy={adding}
              onAdd={(title) => onQuickAdd(title, state)}
            />
          </div>
        </div>
      ))}
    </div>
  );
}

function BoardCard({
  task,
  today,
  onOpen,
}: {
  task: Task;
  today: string;
  onOpen: (id: string) => void;
}) {
  const priority = task.priority || "";
  return (
    <Card
      draggable
      onDragStart={(e) => e.dataTransfer.setData("text/task-id", task.id)}
      onClick={() => onOpen(task.id)}
      className="group cursor-pointer overflow-hidden rounded-md p-0 shadow-sm transition-shadow hover:shadow-md"
    >
      <div className="flex">
        <span
          className="w-1 shrink-0"
          style={{
            backgroundColor: priority
              ? TASK_PRIORITY_COLORS[priority as TaskPriority]
              : "transparent",
          }}
        />
        <div className="min-w-0 flex-1 p-2.5">
          <p className="text-[13px] font-medium leading-snug">{task.title}</p>
          <div className="mt-2 flex flex-wrap items-center gap-1.5">
            {priority && (
              <span
                className="inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium"
                style={{
                  color: TASK_PRIORITY_COLORS[priority as TaskPriority],
                  backgroundColor: `${TASK_PRIORITY_COLORS[priority as TaskPriority]}1a`,
                }}
              >
                {TASK_PRIORITY_LABELS[priority as TaskPriority]}
              </span>
            )}
            <DueBadge due={task.due} today={today} />
          </div>
          <div className="mt-2 flex items-center justify-between text-[11px] text-muted-foreground">
            <span className="flex min-w-0 items-center gap-1">
              <User className="h-3 w-3 shrink-0" />
              <span className="truncate">{task.owner || "unassigned"}</span>
            </span>
            {task.updatedAt && (
              <span className="shrink-0 tabular-nums">
                {relativeTime(task.updatedAt)}
              </span>
            )}
          </div>
        </div>
      </div>
    </Card>
  );
}

function DueBadge({ due, today }: { due?: string; today: string }) {
  if (!due) return null;
  const s = dueState(due, today);
  const cls =
    s === "overdue"
      ? "text-destructive bg-destructive/10"
      : s === "today" || s === "soon"
        ? "text-amber-600 bg-amber-500/10 dark:text-amber-400"
        : "text-muted-foreground bg-muted";
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium",
        cls,
      )}
      title={s === "overdue" ? "Overdue" : "Due"}
    >
      <Calendar className="h-2.5 w-2.5" />
      {formatDue(due)}
    </span>
  );
}

// QuickAdd is the Trello/Notion "+ Add task" at the foot of a column: a one-click
// affordance that expands to a title field and creates a task directly into that
// column's state.
function QuickAdd({
  state,
  busy,
  onAdd,
}: {
  state: TaskState;
  busy: boolean;
  onAdd: (title: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState("");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = title.trim();
    if (!trimmed) return;
    onAdd(trimmed);
    setTitle("");
    // The field stays mounted and focused, so rapid entry continues (Notion/Trello).
  };

  if (!open) {
    return (
      <button
        onClick={() => setOpen(true)}
        className="flex items-center gap-1 rounded-md px-2 py-1.5 text-left text-[12px] text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
      >
        <Plus className="h-3.5 w-3.5" /> Add task
      </button>
    );
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-1.5">
      <Textarea
        autoFocus
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) submit(e);
          if (e.key === "Escape") {
            setOpen(false);
            setTitle("");
          }
        }}
        onBlur={() => {
          if (!title.trim()) setOpen(false);
        }}
        placeholder={`Add to ${TASK_STATE_LABELS[state]}…`}
        rows={2}
        className="resize-none text-[13px]"
      />
      <div className="flex items-center gap-1.5">
        <Button type="submit" disabled={busy || !title.trim()} className="px-2 py-1 text-xs">
          Add
        </Button>
        <button
          type="button"
          onClick={() => {
            setOpen(false);
            setTitle("");
          }}
          className="rounded-sm p-1 text-muted-foreground hover:bg-accent"
          aria-label="Cancel"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    </form>
  );
}

// ── List / management view ─────────────────────────────────────────────────────

type SortKey = "title" | "owner" | "priority" | "due" | "state" | "updated";

function TaskList({
  tasks,
  today,
  onOpen,
  onPatch,
}: {
  tasks: Task[];
  today: string;
  onOpen: (id: string) => void;
  onPatch: (id: string, patch: TaskPatch) => void;
}) {
  const [groupBy, setGroupBy] = useState<"none" | "state" | "owner">("none");
  const [sortKey, setSortKey] = useState<SortKey>("updated");
  const [asc, setAsc] = useState(false);

  const sorted = useMemo(() => {
    const val = (t: Task): string => {
      switch (sortKey) {
        case "title":
          return t.title.toLowerCase();
        case "owner":
          return (t.owner ?? "").toLowerCase();
        case "priority":
          return String(PRIORITY_RANK[t.priority ?? ""]);
        case "due":
          return t.due || "9999-99-99";
        case "state":
          return t.state;
        case "updated":
          return t.updatedAt ?? "";
      }
    };
    const copy = [...tasks].sort((a, b) => {
      const av = val(a);
      const bv = val(b);
      if (av === bv) return a.id < b.id ? -1 : 1;
      return (av < bv ? -1 : 1) * (asc ? 1 : -1);
    });
    return copy;
  }, [tasks, sortKey, asc]);

  const groups = useMemo(() => {
    if (groupBy === "none") return [{ key: "", label: "", tasks: sorted }];
    const map = new Map<string, Task[]>();
    for (const t of sorted) {
      const key = groupBy === "state" ? t.state : t.owner || "unassigned";
      (map.get(key) ?? map.set(key, []).get(key)!).push(t);
    }
    const keys =
      groupBy === "state"
        ? TASK_STATES.filter((s) => map.has(s))
        : [...map.keys()].sort();
    return keys.map((key) => ({
      key,
      label:
        groupBy === "state" ? TASK_STATE_LABELS[key as TaskState] : key,
      tasks: map.get(key)!,
    }));
  }, [sorted, groupBy]);

  const toggleSort = (key: SortKey) => {
    if (sortKey === key) setAsc((v) => !v);
    else {
      setSortKey(key);
      setAsc(key === "title" || key === "owner");
    }
  };

  const header = (key: SortKey, label: string, className?: string) => (
    <th className={cn("px-3 py-2 font-medium", className)}>
      <button
        onClick={() => toggleSort(key)}
        className="inline-flex items-center gap-1 hover:text-foreground"
      >
        {label}
        {sortKey === key && (
          <ChevronDown
            className={cn("h-3 w-3 transition-transform", asc && "rotate-180")}
          />
        )}
      </button>
    </th>
  );

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <span>Group by</span>
        <Select
          value={groupBy}
          onChange={(e) =>
            setGroupBy(e.target.value as "none" | "state" | "owner")
          }
          aria-label="Group tasks by"
        >
          <option value="none">None</option>
          <option value="state">State</option>
          <option value="owner">Assignee</option>
        </Select>
      </div>

      {groups.map((group) => (
        <div
          key={group.key || "all"}
          className="overflow-hidden rounded-md border border-border"
        >
          {groupBy !== "none" && (
            <div className="flex items-center gap-2 border-b border-border bg-muted/50 px-3 py-1.5 text-xs font-semibold">
              {groupBy === "state" && (
                <span
                  className="h-2 w-2 rounded-full"
                  style={{
                    backgroundColor: TASK_STATE_COLORS[group.key as TaskState],
                  }}
                />
              )}
              {group.label}
              <span className="font-normal text-muted-foreground tabular-nums">
                {group.tasks.length}
              </span>
            </div>
          )}
          <table className="w-full text-sm">
            {group === groups[0] && (
              <thead className="bg-muted/30 text-left text-[11px] uppercase tracking-wide text-muted-foreground">
                <tr>
                  {header("title", "Task")}
                  {header("owner", "Assignee")}
                  {header("priority", "Priority")}
                  {header("due", "Due")}
                  {header("state", "State")}
                  {header("updated", "Updated")}
                </tr>
              </thead>
            )}
            <tbody className="divide-y divide-border">
              {group.tasks.map((task) => (
                <tr
                  key={task.id}
                  className="cursor-pointer hover:bg-accent/40"
                  onClick={() => onOpen(task.id)}
                >
                  <td className="px-3 py-2">
                    <span className="font-medium">{task.title}</span>
                    {task.derivedStatus && task.derivedStatus !== task.state && (
                      <span className="ml-2 text-[11px] text-muted-foreground">
                        (runtime: {task.derivedStatus})
                      </span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-muted-foreground">
                    {task.owner || "—"}
                  </td>
                  <td
                    className="px-3 py-2"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <Select
                      value={task.priority || ""}
                      onChange={(e) =>
                        onPatch(task.id, {
                          priority: e.target.value as TaskPriority | "",
                        })
                      }
                      aria-label={`Priority of ${task.title}`}
                    >
                      <option value="">—</option>
                      {TASK_PRIORITIES.map((p) => (
                        <option key={p} value={p}>
                          {TASK_PRIORITY_LABELS[p]}
                        </option>
                      ))}
                    </Select>
                  </td>
                  <td className="px-3 py-2">
                    <DueBadge due={task.due} today={today} />
                  </td>
                  <td
                    className="px-3 py-2"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <Select
                      value={task.state}
                      onChange={(e) =>
                        onPatch(task.id, {
                          state: e.target.value as TaskState,
                        })
                      }
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
      ))}
    </div>
  );
}

// ── Detail drawer ──────────────────────────────────────────────────────────────

function TaskDrawer({
  task,
  children,
  today,
  saving,
  deleting,
  onClose,
  onSave,
  onDelete,
  onOpen,
}: {
  task: Task;
  children: Task[];
  today: string;
  saving: boolean;
  deleting: boolean;
  onClose: () => void;
  onSave: (patch: TaskPatch) => void;
  onDelete: () => void;
  onOpen: (id: string) => void;
}) {
  // Local draft, re-seeded whenever a different task is opened so the form never
  // shows a stale task's fields.
  const [title, setTitle] = useState(task.title);
  const [state, setState] = useState<TaskState>(task.state);
  const [priority, setPriority] = useState<TaskPriority | "">(task.priority || "");
  const [due, setDue] = useState(task.due ?? "");
  const [owner, setOwner] = useState(task.owner ?? "");
  const [parent, setParent] = useState(task.parent ?? "");
  const [body, setBody] = useState(task.body ?? "");
  const [acceptance, setAcceptance] = useState(task.acceptance ?? "");
  const [confirmDelete, setConfirmDelete] = useState(false);

  useEffect(() => {
    setTitle(task.title);
    setState(task.state);
    setPriority(task.priority || "");
    setDue(task.due ?? "");
    setOwner(task.owner ?? "");
    setParent(task.parent ?? "");
    setBody(task.body ?? "");
    setAcceptance(task.acceptance ?? "");
    setConfirmDelete(false);
  }, [task.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const dirty =
    title !== task.title ||
    state !== task.state ||
    priority !== (task.priority || "") ||
    due !== (task.due ?? "") ||
    owner !== (task.owner ?? "") ||
    parent !== (task.parent ?? "") ||
    body !== (task.body ?? "") ||
    acceptance !== (task.acceptance ?? "");

  const save = () => {
    if (!title.trim()) return;
    onSave({
      title: title.trim(),
      state,
      priority,
      due,
      owner,
      parent,
      body,
      acceptance,
    });
  };

  return (
    <>
      <div
        className="absolute inset-0 z-20 bg-black/20"
        onClick={onClose}
        aria-hidden
      />
      <aside className="absolute inset-y-0 right-0 z-30 flex w-full max-w-md flex-col border-l border-border bg-background shadow-xl">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <span className="flex items-center gap-2 text-xs text-muted-foreground">
            <span
              className="h-2 w-2 rounded-full"
              style={{ backgroundColor: TASK_STATE_COLORS[state] }}
            />
            Task
            <code className="rounded bg-muted px-1 py-0.5 text-[10px]">
              {task.id}
            </code>
          </span>
          <button
            onClick={onClose}
            className="rounded-sm p-1 text-muted-foreground hover:bg-accent"
            aria-label="Close"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4">
          <Textarea
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            rows={2}
            className="resize-none text-base font-medium"
            placeholder="Task title"
            aria-label="Title"
          />

          <div className="grid grid-cols-2 gap-3">
            <Field label="State">
              <Select
                value={state}
                onChange={(e) => setState(e.target.value as TaskState)}
                className="w-full"
              >
                {TASK_STATES.map((s) => (
                  <option key={s} value={s}>
                    {TASK_STATE_LABELS[s]}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Priority">
              <Select
                value={priority}
                onChange={(e) =>
                  setPriority(e.target.value as TaskPriority | "")
                }
                className="w-full"
              >
                <option value="">None</option>
                {TASK_PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {TASK_PRIORITY_LABELS[p]}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Assignee">
              <Input
                value={owner}
                onChange={(e) => setOwner(e.target.value)}
                placeholder="agent name"
              />
            </Field>
            <Field label="Due">
              <Input
                type="date"
                value={due}
                onChange={(e) => setDue(e.target.value)}
              />
            </Field>
          </div>

          {due && dueState(due, today) === "overdue" && (
            <p className="-mt-2 text-[11px] text-destructive">Overdue</p>
          )}

          <Field label="Parent">
            <Input
              value={parent}
              onChange={(e) => setParent(e.target.value)}
              placeholder="parent task or agent id"
            />
          </Field>

          <Field label="Notes">
            <Textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              rows={5}
              className="resize-y"
              placeholder="Describe the work, context, links…"
            />
          </Field>

          <Field label="Acceptance">
            <Textarea
              value={acceptance}
              onChange={(e) => setAcceptance(e.target.value)}
              rows={3}
              className="resize-y"
              placeholder="What proves this task is done?"
            />
          </Field>

          {children.length > 0 && (
            <Field label={`Subtasks (${children.length})`}>
              <div className="flex flex-col gap-1">
                {children.map((c) => (
                  <button
                    key={c.id}
                    onClick={() => onOpen(c.id)}
                    className="flex items-center gap-2 rounded-sm border border-border px-2 py-1.5 text-left text-[13px] hover:bg-accent"
                  >
                    <span
                      className="h-2 w-2 shrink-0 rounded-full"
                      style={{ backgroundColor: TASK_STATE_COLORS[c.state] }}
                    />
                    <span className="truncate">{c.title}</span>
                  </button>
                ))}
              </div>
            </Field>
          )}

          <div className="text-[11px] text-muted-foreground">
            {task.createdAt && <>Created {relativeTime(task.createdAt)}. </>}
            {task.updatedAt && <>Updated {relativeTime(task.updatedAt)}.</>}
          </div>
        </div>

        <div className="flex items-center justify-between gap-2 border-t border-border px-4 py-3">
          {confirmDelete ? (
            <div className="flex items-center gap-2">
              <Button
                variant="destructive"
                onClick={onDelete}
                disabled={deleting}
              >
                {deleting ? <Spinner className="h-3.5 w-3.5" /> : "Confirm delete"}
              </Button>
              <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
                Cancel
              </Button>
            </div>
          ) : (
            <Button
              variant="ghost"
              onClick={() => setConfirmDelete(true)}
              className="text-destructive hover:bg-destructive/10"
            >
              <Trash2 className="h-3.5 w-3.5" /> Delete
            </Button>
          )}
          <Button onClick={save} disabled={!dirty || saving || !title.trim()}>
            {saving ? <Spinner className="h-3.5 w-3.5" /> : "Save"}
          </Button>
        </div>
      </aside>
    </>
  );
}

function Field({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </span>
      {children}
    </label>
  );
}
