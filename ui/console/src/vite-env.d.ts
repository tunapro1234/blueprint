/// <reference types="vite/client" />

declare global {
  interface Window {
    // Injected by the bp daemon (internal/dashboard/console.go) with the loopback
    // API token, or left as the literal placeholder under `npm run dev`.
    __BP_TOKEN__?: string;
  }
}

export {};
