// AppShell is the console frame: a left sidebar (Org, Tasks, Usage, Workflows),
// a header, and the routed page. Dark/light is a class on <html>, persisted per
// viewer in localStorage.

import { useState } from "react";
import { NavLink, Outlet } from "react-router-dom";
import {
  Network,
  ListTodo,
  Gauge,
  Workflow,
  Moon,
  Sun,
  CircleDot,
} from "lucide-react";
import type { ComponentType } from "react";

import { cn } from "../lib/utils";

interface NavItem {
  to: string;
  label: string;
  icon: ComponentType<{ className?: string }>;
}

const NAV: NavItem[] = [
  { to: "/org", label: "Org", icon: Network },
  { to: "/tasks", label: "Tasks", icon: ListTodo },
  { to: "/usage", label: "Usage", icon: Gauge },
  { to: "/workflows", label: "Workflows", icon: Workflow },
];

function ThemeToggle() {
  const [dark, setDark] = useState(() =>
    document.documentElement.classList.contains("dark"),
  );
  const toggle = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
    try {
      localStorage.setItem("bp-console-theme", next ? "dark" : "light");
    } catch {
      // Private mode or blocked storage: the theme still applies this session.
    }
  };
  return (
    <button
      onClick={toggle}
      className="inline-flex h-7 w-7 items-center justify-center rounded-sm border border-border text-muted-foreground hover:bg-accent"
      aria-label={dark ? "Switch to light theme" : "Switch to dark theme"}
      title={dark ? "Light theme" : "Dark theme"}
    >
      {dark ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
    </button>
  );
}

export function AppShell() {
  return (
    <div className="flex h-full w-full">
      <aside className="flex w-56 shrink-0 flex-col border-r border-sidebar-border bg-sidebar">
        <div className="flex items-center gap-2 px-4 py-3.5">
          <CircleDot className="h-4 w-4 text-foreground" />
          <span className="text-sm font-semibold tracking-tight">
            bp fleet console
          </span>
        </div>
        <nav className="flex flex-1 flex-col gap-0.5 px-2 py-2">
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-2.5 rounded-sm px-3 py-2 text-[13px] transition-colors",
                  isActive
                    ? "bg-accent font-medium text-accent-foreground"
                    : "text-muted-foreground hover:bg-accent hover:text-foreground",
                )
              }
            >
              <item.icon className="h-4 w-4" />
              {item.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-sidebar-border px-4 py-3 text-[11px] text-muted-foreground">
          <p>Reads bp live state. Status is never guessed.</p>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center justify-between border-b border-border px-5 py-2.5">
          <div className="text-xs text-muted-foreground">
            Local console · 127.0.0.1
          </div>
          <ThemeToggle />
        </header>
        <main className="min-h-0 flex-1 overflow-hidden">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
