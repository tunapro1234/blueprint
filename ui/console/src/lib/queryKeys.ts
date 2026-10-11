// queryKeys centralizes every TanStack Query key so invalidation after a
// mutation stays consistent across the app (mirrors Paperclip's queryKeys.ts).

export const queryKeys = {
  fleet: {
    tree: () => ["fleet", "tree"] as const,
    layout: () => ["fleet", "layout"] as const,
  },
  tasks: {
    list: (filter: { owner?: string; state?: string; parent?: string }) =>
      ["tasks", "list", filter] as const,
    detail: (id: string) => ["tasks", "detail", id] as const,
  },
  usage: {
    accounts: () => ["usage", "accounts"] as const,
  },
  workflows: {
    list: () => ["workflows", "list"] as const,
    detail: (name: string) => ["workflows", "detail", name] as const,
  },
} as const;
