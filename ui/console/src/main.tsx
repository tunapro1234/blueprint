import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter } from "react-router-dom";

import "./index.css";
import { App } from "./App";

// Apply the saved theme before first paint so the shell never flashes.
const savedTheme = localStorage.getItem("bp-console-theme");
const prefersDark =
  savedTheme === "dark" ||
  (savedTheme === null &&
    window.matchMedia("(prefers-color-scheme: dark)").matches);
document.documentElement.classList.toggle("dark", prefersDark);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchInterval: 15_000, // the fleet view stays close to live
      refetchOnWindowFocus: true,
      staleTime: 5_000,
      retry: 1,
    },
  },
});

const root = document.getElementById("root");
if (!root) throw new Error("missing #root element");

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <App />
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
);
