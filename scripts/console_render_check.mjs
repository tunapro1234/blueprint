// console_render_check.mjs — headless render proof for the fleet console.
//
// Loads each SPA route in a real browser, waits for React + React Query to
// mount and fetch, screenshots the page, and asserts that #root actually
// rendered content (the nav shell plus a per-page marker) rather than a blank
// mount point. This is the evidence that the console *renders*, not just that
// its HTTP endpoints answer.
//
// Usage:  node scripts/console_render_check.mjs <base-url> <out-dir>
// Needs Playwright (or playwright-core) resolvable by node, and a Chromium.
// If Playwright ships no browser, set PW_CHROMIUM=/path/to/chromium (any
// system Chromium/Chrome works) and it will be used via executablePath.

import { mkdirSync } from "node:fs";

const baseUrl = (process.argv[2] || "").replace(/\/$/, "");
const outDir = process.argv[3] || "console-shots";
if (!baseUrl) {
  console.error("usage: node console_render_check.mjs <base-url> <out-dir>");
  process.exit(2);
}
mkdirSync(outDir, { recursive: true });

// Resolve Playwright: prefer the full package, fall back to playwright-core.
let chromium;
try {
  ({ chromium } = await import("playwright"));
} catch {
  try {
    ({ chromium } = await import("playwright-core"));
  } catch (e) {
    console.error("RENDER FAIL: Playwright not resolvable by node:", e.message);
    console.error("  install it (npm i -D playwright) or run with a NODE_PATH that has it.");
    process.exit(3);
  }
}

// Launch defensively: a Playwright-managed browser first, then a system
// Chromium by channel or explicit executablePath.
async function launch() {
  const attempts = [
    () => chromium.launch({ headless: true }),
    () => chromium.launch({ headless: true, channel: "chromium" }),
    () => chromium.launch({ headless: true, channel: "chrome" }),
  ];
  if (process.env.PW_CHROMIUM) {
    attempts.push(() =>
      chromium.launch({ headless: true, executablePath: process.env.PW_CHROMIUM }),
    );
  }
  let lastErr;
  for (const attempt of attempts) {
    try {
      return await attempt();
    } catch (e) {
      lastErr = e;
    }
  }
  throw lastErr;
}

// Each page: the route, the nav label that proves the shell mounted, and a
// per-page marker that proves the page component itself rendered (its heading,
// or its known empty-state — both are real renders over live data).
const PAGES = [
  { path: "/org", label: "org", markers: ["Org"] },
  { path: "/tasks", label: "tasks", markers: ["Tasks"] },
  { path: "/usage", label: "usage", markers: ["Usage"] },
  { path: "/workflows", label: "workflows", markers: ["Workflows"] },
];
const NAV = ["Org", "Tasks", "Usage", "Workflows"];

let browser;
try {
  browser = await launch();
} catch (e) {
  console.error("RENDER FAIL: could not launch a browser:", e.message);
  console.error("  set PW_CHROMIUM=/path/to/chromium (system Chromium is fine).");
  process.exit(3);
}

const results = [];
let allOk = true;
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });

for (const spec of PAGES) {
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  const url = baseUrl + spec.path;
  let ok = false;
  let text = "";
  try {
    await page.goto(url, { waitUntil: "networkidle", timeout: 20000 });
    // Wait until the SPA actually painted something into #root.
    await page.waitForFunction(
      () => {
        const r = document.getElementById("root");
        return r && r.textContent && r.textContent.trim().length > 0;
      },
      { timeout: 15000 },
    );
    text = (await page.innerText("body")).replace(/\s+/g, " ").trim();
    const shot = `${outDir}/${spec.label}.png`;
    await page.screenshot({ path: shot, fullPage: true });
    const navOk = NAV.every((n) => text.includes(n));
    const markerOk = spec.markers.every((m) => text.includes(m));
    ok = navOk && markerOk && errors.length === 0;
    results.push({
      path: spec.path,
      ok,
      navOk,
      markerOk,
      jsErrors: errors,
      shot,
      textHead: text.slice(0, 220),
    });
  } catch (e) {
    results.push({ path: spec.path, ok: false, error: String(e), jsErrors: errors });
  }
  if (!ok) allOk = false;
  await page.close();
}

await browser.close();

console.log("=== RENDER RESULTS ===");
for (const r of results) {
  console.log(
    `${r.ok ? "OK  " : "FAIL"} ${r.path}` +
      (r.error ? `  error=${r.error}` : "") +
      (r.jsErrors && r.jsErrors.length ? `  jsErrors=${JSON.stringify(r.jsErrors)}` : ""),
  );
  if (r.textHead) console.log(`      text: ${r.textHead}`);
  if (r.shot) console.log(`      shot: ${r.shot}`);
}
console.log(allOk ? "RENDER PASS: all pages mounted and rendered." : "RENDER FAIL: see above.");
process.exit(allOk ? 0 : 1);
