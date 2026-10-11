// OrgPage draws bp's live fleet as a tidy org chart, plus the three bp-native
// extensions the owner asked for: drag a card onto another to reparent it
// (POST /v1/fleet/reparent), drag a card onto empty canvas to free-move it
// (persisted via /v1/fleet/layout), and a "group by host" toggle that draws one
// zone box per machine.
//
// Layout notes. A real fleet is deep but very fan-out heavy: a handful of
// parents own dozens of leaf workers. A plain tidy tree lays every sibling in
// one row, which makes the chart "short but extremely wide" and unreadable on a
// 1920x1080 screen. Two measures fix that and fill the space efficiently:
//   1. leaf clustering — when a node has >= CLUSTER_MIN leaf children (agents
//      with no reports of their own), they are packed into a compact grid block
//      with a single edge to the parent, instead of one very wide row;
//   2. forest shelf-packing — root subtrees are wrapped into rows whose width
//      targets a ~16:9 aspect, instead of growing left-to-right forever.

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  type WheelEvent as ReactWheelEvent,
} from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Network, Plus, Minus, Maximize2, Server, GitBranch } from "lucide-react";

import { fleetApi } from "../lib/api";
import { queryKeys } from "../lib/queryKeys";
import type { FleetAgent, LayoutPos } from "../lib/types";
import { agentStatusColor, cn, relativeTime } from "../lib/utils";
import { Button, PageState, Spinner } from "../components/ui";

const CARD_W = 200;
const CARD_H = 100;
const GAP_X = 32;
const GAP_Y = 72;
const PADDING = 60;
const MIN_ZOOM = 0.2;
const MAX_ZOOM = 2;
const LEAF_COLS_MAX = 6; // widest a leaf cluster grid may get
const CLUSTER_MIN = 3; // >= this many leaf children render as a boxed grid

interface OrgNode {
  name: string;
  agent: FleetAgent;
  children: OrgNode[];
}

type PosMap = Record<string, LayoutPos>;
interface Size {
  w: number;
  h: number;
}
interface Cluster {
  parent: string;
  members: string[];
  x: number;
  y: number;
  w: number;
  h: number;
}

// buildForest turns the flat agent list into a parent→children forest. A parent
// that is not itself in the set (or "") makes its agent a root, so a live but
// unregistered agent still shows. A visited guard keeps a malformed cycle from
// looping forever.
function buildForest(agents: FleetAgent[]): OrgNode[] {
  const byName = new Map<string, OrgNode>();
  for (const agent of agents) {
    byName.set(agent.name, { name: agent.name, agent, children: [] });
  }
  const roots: OrgNode[] = [];
  for (const agent of agents) {
    const node = byName.get(agent.name)!;
    const parent = agent.parent ? byName.get(agent.parent) : undefined;
    if (parent && parent.name !== node.name) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }
  // Stable order within each level.
  const sortRec = (node: OrgNode) => {
    node.children.sort((a, b) => a.name.localeCompare(b.name));
    node.children.forEach(sortRec);
  };
  roots.sort((a, b) => a.name.localeCompare(b.name));
  roots.forEach(sortRec);
  return roots;
}

// splitChildren separates children that have their own descendants (branches,
// laid as tidy subtrees) from leaves (candidates for grid clustering).
function splitChildren(node: OrgNode): { branches: OrgNode[]; leaves: OrgNode[] } {
  const branches: OrgNode[] = [];
  const leaves: OrgNode[] = [];
  for (const c of node.children) {
    if (c.children.length > 0) branches.push(c);
    else leaves.push(c);
  }
  return { branches, leaves };
}

function gridCols(n: number): number {
  return Math.min(LEAF_COLS_MAX, Math.max(2, Math.ceil(Math.sqrt(n))));
}

function gridDims(n: number): { cols: number; rows: number; w: number; h: number } {
  const cols = gridCols(n);
  const rows = Math.ceil(n / cols);
  return {
    cols,
    rows,
    w: cols * CARD_W + (cols - 1) * GAP_X,
    h: rows * CARD_H + (rows - 1) * GAP_Y,
  };
}

// measureAll fills `sizes` bottom-up with each subtree's bounding box, treating a
// leaf cluster (>= CLUSTER_MIN leaves) as a single grid block within the row.
function measureAll(node: OrgNode, sizes: Map<string, Size>): Size {
  if (node.children.length === 0) {
    const s = { w: CARD_W, h: CARD_H };
    sizes.set(node.name, s);
    return s;
  }
  const { branches, leaves } = splitChildren(node);
  for (const b of branches) measureAll(b, sizes);
  for (const l of leaves) sizes.set(l.name, { w: CARD_W, h: CARD_H });

  const clustered = leaves.length >= CLUSTER_MIN;
  const rowKids = clustered ? branches : [...branches, ...leaves];
  let rowW =
    rowKids.reduce((s, c) => s + sizes.get(c.name)!.w, 0) +
    Math.max(0, rowKids.length - 1) * GAP_X;
  let rowH = rowKids.reduce((h, c) => Math.max(h, sizes.get(c.name)!.h), CARD_H);
  if (clustered) {
    const g = gridDims(leaves.length);
    if (rowKids.length > 0) rowW += GAP_X;
    rowW += g.w;
    rowH = Math.max(rowH, g.h);
  }
  const s = { w: Math.max(CARD_W, rowW), h: CARD_H + GAP_Y + rowH };
  sizes.set(node.name, s);
  return s;
}

// placeTree positions `node` and its descendants, appending any leaf clusters to
// `clusters`. Branch subtrees and small leaf sets sit in the child row; a large
// leaf set becomes a boxed grid placed after them.
function placeTree(
  node: OrgNode,
  x: number,
  y: number,
  sizes: Map<string, Size>,
  out: PosMap,
  clusters: Cluster[],
) {
  const self = sizes.get(node.name)!;
  out[node.name] = { x: x + (self.w - CARD_W) / 2, y };
  if (node.children.length === 0) return;

  const { branches, leaves } = splitChildren(node);
  const clustered = leaves.length >= CLUSTER_MIN;
  const rowKids = clustered ? branches : [...branches, ...leaves];
  const g = clustered ? gridDims(leaves.length) : null;

  let rowW =
    rowKids.reduce((s, c) => s + sizes.get(c.name)!.w, 0) +
    Math.max(0, rowKids.length - 1) * GAP_X;
  if (g) {
    if (rowKids.length > 0) rowW += GAP_X;
    rowW += g.w;
  }
  rowW = Math.max(CARD_W, rowW);

  let cx = x + (self.w - rowW) / 2;
  const cy = y + CARD_H + GAP_Y;
  for (const c of rowKids) {
    placeTree(c, cx, cy, sizes, out, clusters);
    cx += sizes.get(c.name)!.w + GAP_X;
  }
  if (g) {
    const gx = cx;
    clusters.push({
      parent: node.name,
      members: leaves.map((l) => l.name),
      x: gx,
      y: cy,
      w: g.w,
      h: g.h,
    });
    leaves.forEach((c, i) => {
      const col = i % g.cols;
      const row = Math.floor(i / g.cols);
      out[c.name] = { x: gx + col * (CARD_W + GAP_X), y: cy + row * (CARD_H + GAP_Y) };
    });
  }
}

// layoutForest lays each root subtree, then shelf-packs the roots into rows whose
// total width targets a ~16:9 aspect so the chart fills a 1920x1080 viewport
// instead of growing into one short, very wide strip.
function layoutForest(roots: OrgNode[]): { positions: PosMap; clusters: Cluster[] } {
  const sizes = new Map<string, Size>();
  const rootSizes = roots.map((r) => measureAll(r, sizes));
  const area = rootSizes.reduce((s, m) => s + m.w * m.h, 0);
  const widest = rootSizes.reduce((m, s) => Math.max(m, s.w), CARD_W);
  const target = Math.max(widest, Math.sqrt(area * (16 / 9)));

  const positions: PosMap = {};
  const clusters: Cluster[] = [];
  let x = PADDING;
  let y = PADDING;
  let rowH = 0;
  for (let i = 0; i < roots.length; i++) {
    const m = rootSizes[i];
    if (x > PADDING && x - PADDING + m.w > target) {
      x = PADDING;
      y += rowH + GAP_Y * 2;
      rowH = 0;
    }
    placeTree(roots[i], x, y, sizes, positions, clusters);
    x += m.w + GAP_X * 2;
    rowH = Math.max(rowH, m.h);
  }
  return { positions, clusters };
}

interface HostZone {
  host: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// hostLayout groups agents into per-machine zones and grid-lays the cards inside
// each. It ignores free positions and the reporting edges: this view answers
// "what runs where", not "who reports to whom".
function hostLayout(agents: FleetAgent[]): { positions: PosMap; zones: HostZone[] } {
  const hosts = new Map<string, FleetAgent[]>();
  for (const agent of agents) {
    const host = agent.host || "unknown";
    const list = hosts.get(host) ?? [];
    list.push(agent);
    hosts.set(host, list);
  }
  const positions: PosMap = {};
  const zones: HostZone[] = [];
  const zonePad = 24;
  const cols = 4;
  let y = PADDING;
  for (const host of [...hosts.keys()].sort()) {
    const list = hosts.get(host)!.sort((a, b) => a.name.localeCompare(b.name));
    const rows = Math.ceil(list.length / cols);
    const zoneW = cols * CARD_W + (cols - 1) * GAP_X + zonePad * 2;
    const zoneH = rows * CARD_H + (rows - 1) * GAP_Y + zonePad * 2 + 28;
    list.forEach((agent, i) => {
      const col = i % cols;
      const row = Math.floor(i / cols);
      positions[agent.name] = {
        x: PADDING + zonePad + col * (CARD_W + GAP_X),
        y: y + zonePad + 28 + row * (CARD_H + GAP_Y),
      };
    });
    zones.push({ host, x: PADDING, y, w: zoneW, h: zoneH });
    y += zoneH + GAP_Y;
  }
  return { positions, zones };
}

export function OrgPage() {
  const queryClient = useQueryClient();
  const containerRef = useRef<HTMLDivElement>(null);

  const treeQuery = useQuery({
    queryKey: queryKeys.fleet.tree(),
    queryFn: fleetApi.tree,
  });
  const layoutQuery = useQuery({
    queryKey: queryKeys.fleet.layout(),
    queryFn: fleetApi.layout,
  });

  const agents = useMemo(() => treeQuery.data?.agents ?? [], [treeQuery.data]);
  const [groupByHost, setGroupByHost] = useState(false);

  // Free-move positions, seeded from the persisted layout and updated locally on
  // drop so the card does not jump before the save round-trips.
  const [free, setFree] = useState<PosMap>({});
  useEffect(() => {
    if (layoutQuery.data?.positions) setFree(layoutQuery.data.positions);
  }, [layoutQuery.data]);

  const forest = useMemo(() => buildForest(agents), [agents]);
  const layout = useMemo(() => layoutForest(forest), [forest]);
  const tidy = layout.positions;
  const clusters = layout.clusters;
  const clusteredSet = useMemo(
    () => new Set(clusters.flatMap((c) => c.members)),
    [clusters],
  );
  const host = useMemo(() => hostLayout(agents), [agents]);

  const reparentMut = useMutation({
    mutationFn: ({ agent, parent }: { agent: string; parent: string }) =>
      fleetApi.reparent(agent, parent),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: queryKeys.fleet.tree() }),
  });
  const saveLayoutMut = useMutation({
    mutationFn: (positions: PosMap) => fleetApi.saveLayout(positions),
  });

  // Effective position of a card for the current mode.
  const posOf = useCallback(
    (name: string, dragged: { name: string; x: number; y: number } | null): LayoutPos => {
      if (dragged && dragged.name === name) return { x: dragged.x, y: dragged.y };
      if (groupByHost) return host.positions[name] ?? { x: 0, y: 0 };
      return free[name] ?? tidy[name] ?? { x: 0, y: 0 };
    },
    [groupByHost, host.positions, free, tidy],
  );

  // ── Pan / zoom ─────────────────────────────────────────────────────────────
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const [zoom, setZoom] = useState(1);
  const [dragged, setDragged] = useState<{ name: string; x: number; y: number } | null>(null);
  const panRef = useRef({ panning: false, startX: 0, startY: 0, panX: 0, panY: 0 });
  const cardRef = useRef<
    { name: string; startX: number; startY: number; originX: number; originY: number; moved: boolean } | null
  >(null);

  const bounds = useMemo(() => {
    const names = agents.map((a) => a.name);
    if (names.length === 0) return { width: 800, height: 600 };
    let maxX = 0;
    let maxY = 0;
    for (const name of names) {
      const p = groupByHost ? host.positions[name] : free[name] ?? tidy[name];
      if (!p) continue;
      maxX = Math.max(maxX, p.x + CARD_W);
      maxY = Math.max(maxY, p.y + CARD_H);
    }
    if (groupByHost) {
      for (const z of host.zones) {
        maxX = Math.max(maxX, z.x + z.w);
        maxY = Math.max(maxY, z.y + z.h);
      }
    }
    return { width: maxX + PADDING, height: maxY + PADDING };
  }, [agents, groupByHost, host, free, tidy]);

  const fit = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    const cw = el.clientWidth;
    const ch = el.clientHeight;
    const fitZoom = Math.max(
      MIN_ZOOM,
      Math.min((cw - 40) / bounds.width, (ch - 40) / bounds.height, 1),
    );
    setZoom(fitZoom);
    setPan({
      x: (cw - bounds.width * fitZoom) / 2,
      y: (ch - bounds.height * fitZoom) / 2,
    });
  }, [bounds]);

  // Re-fit when the data (and thus bounds) first arrives or the mode changes.
  const fitKey = `${groupByHost}:${agents.length}`;
  const lastFit = useRef("");
  useEffect(() => {
    if (agents.length === 0 || !containerRef.current) return;
    if (lastFit.current === fitKey) return;
    lastFit.current = fitKey;
    fit();
  }, [fitKey, agents.length, fit]);

  const zoomTo = useCallback(
    (nextZoom: number, cx: number, cy: number) => {
      setPan((p) => {
        const scale = nextZoom / zoom;
        return { x: cx - scale * (cx - p.x), y: cy - scale * (cy - p.y) };
      });
      setZoom(nextZoom);
    },
    [zoom],
  );

  const onWheel = useCallback(
    (e: ReactWheelEvent<HTMLDivElement>) => {
      const el = containerRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      const factor = e.deltaY < 0 ? 1.1 : 0.9;
      const next = Math.min(Math.max(zoom * factor, MIN_ZOOM), MAX_ZOOM);
      zoomTo(next, e.clientX - rect.left, e.clientY - rect.top);
    },
    [zoom, zoomTo],
  );

  const onMouseDown = useCallback(
    (e: ReactMouseEvent<HTMLDivElement>) => {
      if (e.button !== 0) return;
      const target = e.target as HTMLElement;
      const card = target.closest("[data-card]") as HTMLElement | null;
      if (card && !groupByHost) {
        const name = card.dataset.card!;
        const origin = free[name] ?? tidy[name] ?? { x: 0, y: 0 };
        cardRef.current = {
          name,
          startX: e.clientX,
          startY: e.clientY,
          originX: origin.x,
          originY: origin.y,
          moved: false,
        };
        return;
      }
      panRef.current = {
        panning: true,
        startX: e.clientX,
        startY: e.clientY,
        panX: pan.x,
        panY: pan.y,
      };
    },
    [groupByHost, free, tidy, pan],
  );

  const onMouseMove = useCallback(
    (e: ReactMouseEvent<HTMLDivElement>) => {
      const cd = cardRef.current;
      if (cd) {
        const dx = (e.clientX - cd.startX) / zoom;
        const dy = (e.clientY - cd.startY) / zoom;
        if (Math.abs(dx) > 2 || Math.abs(dy) > 2) cd.moved = true;
        setDragged({ name: cd.name, x: cd.originX + dx, y: cd.originY + dy });
        return;
      }
      const pd = panRef.current;
      if (pd.panning) {
        setPan({ x: pd.panX + (e.clientX - pd.startX), y: pd.panY + (e.clientY - pd.startY) });
      }
    },
    [zoom],
  );

  const endGesture = useCallback(
    (e: ReactMouseEvent<HTMLDivElement>) => {
      const cd = cardRef.current;
      cardRef.current = null;
      panRef.current.panning = false;
      if (!cd) return;
      if (!cd.moved) {
        setDragged(null);
        return;
      }
      const el = containerRef.current;
      if (!el) {
        setDragged(null);
        return;
      }
      const rect = el.getBoundingClientRect();
      const worldX = (e.clientX - rect.left - pan.x) / zoom;
      const worldY = (e.clientY - rect.top - pan.y) / zoom;
      // Did we drop onto another card? If so, reparent; otherwise free-move.
      let target: string | null = null;
      for (const agent of agents) {
        if (agent.name === cd.name) continue;
        const p = free[agent.name] ?? tidy[agent.name];
        if (!p) continue;
        if (
          worldX >= p.x &&
          worldX <= p.x + CARD_W &&
          worldY >= p.y &&
          worldY <= p.y + CARD_H
        ) {
          target = agent.name;
          break;
        }
      }
      if (target) {
        reparentMut.mutate({ agent: cd.name, parent: target });
        setDragged(null); // the refetched tree reflows the card
        return;
      }
      const next: PosMap = {
        ...free,
        [cd.name]: { x: cd.originX + (e.clientX - cd.startX) / zoom, y: cd.originY + (e.clientY - cd.startY) / zoom },
      };
      setFree(next);
      setDragged(null);
      saveLayoutMut.mutate(next);
    },
    [agents, free, tidy, pan, zoom, reparentMut, saveLayoutMut],
  );

  if (treeQuery.isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (treeQuery.isError) {
    return (
      <PageState
        icon={Network}
        title="Could not load the fleet"
        detail={(treeQuery.error as Error).message}
      />
    );
  }
  if (agents.length === 0) {
    return <PageState icon={Network} title="No agents registered yet." />;
  }

  const edges = groupByHost
    ? []
    : agents
        .filter(
          (a) =>
            a.parent &&
            !clusteredSet.has(a.name) &&
            agents.some((p) => p.name === a.parent),
        )
        .map((a) => ({ parent: a.parent, child: a.name }));

  return (
    <div className="flex h-full flex-col">
      <div className="flex items-center justify-between gap-2 border-b border-border px-5 py-2">
        <div className="flex items-baseline gap-2">
          <h1 className="text-sm font-semibold">Org</h1>
          <span className="text-[11px] text-muted-foreground">{agents.length} agents</span>
        </div>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="hidden md:inline">
            Drag a card onto another to reparent; onto empty space to arrange.
          </span>
          <Button
            variant={groupByHost ? "default" : "outline"}
            onClick={() => setGroupByHost((v) => !v)}
            title="Group agents into one zone per host"
          >
            {groupByHost ? <Server className="h-3.5 w-3.5" /> : <GitBranch className="h-3.5 w-3.5" />}
            {groupByHost ? "By host" : "By report line"}
          </Button>
        </div>
      </div>

      <div
        ref={containerRef}
        className="relative min-h-0 flex-1 overflow-hidden bg-muted/20"
        style={{ cursor: panRef.current.panning ? "grabbing" : "grab" }}
        onMouseDown={onMouseDown}
        onMouseMove={onMouseMove}
        onMouseUp={endGesture}
        onMouseLeave={endGesture}
        onWheel={onWheel}
      >
        <div className="absolute right-3 top-3 z-10 flex flex-col gap-1">
          <ZoomButton label="Zoom in" onClick={() => {
            const el = containerRef.current;
            if (el) zoomTo(Math.min(zoom * 1.2, MAX_ZOOM), el.clientWidth / 2, el.clientHeight / 2);
          }}>
            <Plus className="h-3.5 w-3.5" />
          </ZoomButton>
          <ZoomButton label="Zoom out" onClick={() => {
            const el = containerRef.current;
            if (el) zoomTo(Math.max(zoom * 0.8, MIN_ZOOM), el.clientWidth / 2, el.clientHeight / 2);
          }}>
            <Minus className="h-3.5 w-3.5" />
          </ZoomButton>
          <ZoomButton label="Fit to screen" onClick={fit}>
            <Maximize2 className="h-3.5 w-3.5" />
          </ZoomButton>
        </div>

        {/* Host zones */}
        {groupByHost && (
          <div
            className="absolute inset-0"
            style={{ transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`, transformOrigin: "0 0" }}
          >
            {host.zones.map((z) => (
              <div
                key={z.host}
                className="absolute border border-dashed border-border bg-background/40"
                style={{ left: z.x, top: z.y, width: z.w, height: z.h }}
              >
                <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-muted-foreground">
                  <Server className="h-3.5 w-3.5" />
                  {z.host}
                </div>
              </div>
            ))}
          </div>
        )}

        {/* Edge layer */}
        <svg className="pointer-events-none absolute inset-0 h-full w-full">
          <g transform={`translate(${pan.x}, ${pan.y}) scale(${zoom})`}>
            {edges.map((edge) => {
              const p = posOf(edge.parent, dragged);
              const c = posOf(edge.child, dragged);
              const x1 = p.x + CARD_W / 2;
              const y1 = p.y + CARD_H;
              const x2 = c.x + CARD_W / 2;
              const y2 = c.y;
              const midY = (y1 + y2) / 2;
              return (
                <path
                  key={`${edge.parent}-${edge.child}`}
                  d={`M ${x1} ${y1} L ${x1} ${midY} L ${x2} ${midY} L ${x2} ${y2}`}
                  fill="none"
                  stroke="var(--border)"
                  strokeWidth={1.5}
                />
              );
            })}
            {!groupByHost &&
              clusters.map((cl) => {
                const p = posOf(cl.parent, dragged);
                const x1 = p.x + CARD_W / 2;
                const y1 = p.y + CARD_H;
                const x2 = cl.x + cl.w / 2;
                const y2 = cl.y;
                const midY = (y1 + y2) / 2;
                return (
                  <path
                    key={`cluster-${cl.parent}`}
                    d={`M ${x1} ${y1} L ${x1} ${midY} L ${x2} ${midY} L ${x2} ${y2}`}
                    fill="none"
                    stroke="var(--border)"
                    strokeWidth={1.5}
                  />
                );
              })}
          </g>
        </svg>

        {/* Card layer */}
        <div
          className="absolute inset-0"
          style={{ transform: `translate(${pan.x}px, ${pan.y}px) scale(${zoom})`, transformOrigin: "0 0" }}
        >
          {/* Leaf-cluster backing boxes, behind their cards. */}
          {!groupByHost &&
            clusters.map((cl) => (
              <div
                key={`clbox-${cl.parent}`}
                className="absolute rounded-md border border-dashed border-border/70 bg-background/30"
                style={{ left: cl.x - 10, top: cl.y - 10, width: cl.w + 20, height: cl.h + 20 }}
              >
                <span className="absolute -top-2 right-2 rounded-sm bg-muted px-1.5 text-[10px] font-medium text-muted-foreground">
                  {cl.members.length}
                </span>
              </div>
            ))}
          {agents.map((agent) => {
            const p = posOf(agent.name, dragged);
            return <OrgCard key={agent.name} agent={agent} x={p.x} y={p.y} />;
          })}
        </div>
      </div>
    </div>
  );
}

function ZoomButton({
  label,
  onClick,
  children,
}: {
  label: string;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      aria-label={label}
      title={label}
      className="flex h-7 w-7 items-center justify-center rounded-sm border border-border bg-background text-foreground hover:bg-accent"
    >
      {children}
    </button>
  );
}

function OrgCard({ agent, x, y }: { agent: FleetAgent; x: number; y: number }) {
  const dot = agentStatusColor(agent.status);
  return (
    <div
      data-card={agent.name}
      className={cn(
        "absolute select-none border border-border bg-card shadow-sm transition-shadow hover:border-foreground/20 hover:shadow-md",
      )}
      style={{ left: x, top: y, width: CARD_W, minHeight: CARD_H }}
    >
      <div className="flex items-start gap-3 px-4 py-3">
        <div className="relative mt-0.5 shrink-0">
          <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted text-[11px] font-semibold uppercase text-foreground/70">
            {agent.name.slice(0, 2)}
          </div>
          <span
            className="absolute -bottom-0.5 -right-0.5 h-3 w-3 rounded-full border-2 border-card"
            style={{ backgroundColor: dot }}
            title={agent.statusReason || agent.status}
          />
        </div>
        <div className="flex min-w-0 flex-1 flex-col">
          <span className="truncate text-sm font-semibold leading-tight" title={agent.name}>{agent.name}</span>
          <span className="truncate text-[11px] leading-tight text-muted-foreground">
            {agent.role || "—"}
          </span>
          <div className="mt-1 flex items-center gap-1.5 text-[10px] text-muted-foreground/70">
            {agent.runtime && <span className="font-mono">{agent.runtime}</span>}
            {agent.host && <span className="truncate">· {agent.host}</span>}
          </div>
          <div className="mt-1 flex items-center justify-between">
            <span className="text-[10px] capitalize" style={{ color: dot }}>
              {agent.status}
            </span>
            {agent.lastTurnAt && (
              <span className="text-[9px] text-muted-foreground/60 tabular-nums">
                {relativeTime(agent.lastTurnAt)}
              </span>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
