// A lean, local set of shadcn-flavored primitives. Kept small on purpose to
// keep the bundle light; these cover the whole console.

import type {
  ButtonHTMLAttributes,
  ComponentType,
  HTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from "react";
import { Loader2 } from "lucide-react";

import { cn } from "../lib/utils";

type ButtonVariant = "default" | "outline" | "ghost" | "destructive";

const buttonVariants: Record<ButtonVariant, string> = {
  default:
    "bg-primary text-primary-foreground hover:bg-primary/90 border border-transparent",
  outline:
    "bg-background text-foreground border border-border hover:bg-accent",
  ghost: "bg-transparent text-foreground hover:bg-accent border border-transparent",
  destructive:
    "bg-destructive text-destructive-foreground hover:bg-destructive/90 border border-transparent",
};

export function Button({
  variant = "default",
  className,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      className={cn(
        "inline-flex items-center justify-center gap-1.5 rounded-sm px-3 py-1.5 text-[13px] font-medium transition-colors disabled:pointer-events-none disabled:opacity-50",
        buttonVariants[variant],
        className,
      )}
      {...props}
    />
  );
}

export function Card({
  className,
  ...props
}: HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      className={cn("border border-border bg-card text-card-foreground", className)}
      {...props}
    />
  );
}

export function Input({
  className,
  ...props
}: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cn(
        "w-full rounded-sm border border-input bg-background px-2.5 py-1.5 text-sm outline-none focus:ring-2 focus:ring-ring",
        className,
      )}
      {...props}
    />
  );
}

export function Textarea({
  className,
  ...props
}: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn(
        "w-full rounded-sm border border-input bg-background px-2.5 py-1.5 text-sm outline-none focus:ring-2 focus:ring-ring",
        className,
      )}
      {...props}
    />
  );
}

export function Select({
  className,
  ...props
}: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cn(
        "rounded-sm border border-input bg-background px-2 py-1.5 text-sm outline-none focus:ring-2 focus:ring-ring",
        className,
      )}
      {...props}
    />
  );
}

export function Badge({
  className,
  color,
  children,
}: {
  className?: string;
  color?: string;
  children: ReactNode;
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border border-border px-2 py-0.5 text-[11px] font-medium",
        className,
      )}
    >
      {color && (
        <span
          className="h-1.5 w-1.5 rounded-full"
          style={{ backgroundColor: color }}
        />
      )}
      {children}
    </span>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn("h-4 w-4 animate-spin", className)} />;
}

export function PageState({
  icon: Icon,
  title,
  detail,
}: {
  icon?: ComponentType<{ className?: string }>;
  title: string;
  detail?: string;
}) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-2 p-10 text-center">
      {Icon && <Icon className="h-8 w-8 text-muted-foreground" />}
      <p className="text-sm font-medium">{title}</p>
      {detail && <p className="max-w-md text-xs text-muted-foreground">{detail}</p>}
    </div>
  );
}
