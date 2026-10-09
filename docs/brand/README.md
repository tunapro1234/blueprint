# Website preview (`site/next/`)

`site/next/` holds the new landing page as static files. bp.tunapro.xyz serves
the `site/` directory of a `dev` checkout, so after a merge the page is live at
https://bp.tunapro.xyz/next/ while the current homepage (`site/index.html`)
stays as it is. The new page describes behavior that only exists on `dev`
(modules, the zero-change installer, MCP and HTTP), so it should replace the
homepage only together with the first release built from `dev`.

## Source

The design was made in Lovable. Its source is a TanStack Start app in
https://github.com/mr-kaynak/pixel-perfect-showcase-7596 (built from commit
`e574129`). All copy, links and command strings live in `src/content.ts` there;
the command outputs shown on the page are real bp output.

## How `site/next/` was built

1. In a clone of the source repo, `vite.config.ts` was set to build for the
   `/next` path, with a Node server output that is used only once:

   ```ts
   export default defineConfig({
     tanstackStart: { server: { entry: "server" }, router: { basepath: "/next" } },
     nitro: { preset: "node-server" },
     vite: { base: "/next/" },
   });
   ```

2. `bun install --ignore-scripts && bun run build`, then
   `PORT=5299 node .output/server/index.mjs` and save the rendered page:
   `curl -s http://127.0.0.1:5299/next/ -o index.html`. The HTML contains the
   full page text, so agents and readers without JavaScript get the content;
   the browser hydrates it for the animations.
3. Copy `.output/public/assets/` and `favicon.svg` next to `index.html`.
4. Patch `index.html` for the `/next/` location: `/favicon.svg` →
   `/next/favicon.svg`, `/llms.txt` → `/next/llms.txt`, and the Open Graph and
   Twitter images → `https://bp.tunapro.xyz/next/og.png`. Add
   `<link rel="canonical" href="https://bp.tunapro.xyz/">` and a `<noscript>`
   style that shows sections whose scroll animation would otherwise keep them
   at `opacity:0`.
5. `llms.txt` is the agent guide written and checked against real runs for the
   site (Lovable's own file was a five-line stub). `og.png` is rendered from
   [`og-card.html`](og-card.html) at 1200×630 with a headless browser.
6. `site/favicon.svg` is a copy of the page's favicon at the site root: the
   hydrated page also requests `/favicon.svg`.

## Moving it to the homepage at release

Rebuild with `basepath: "/"` and `base: "/"`, copy the output to `site/`
(replacing `site/index.html`), point `llms.txt` and the Open Graph images at the
root, and keep `site/install.sh`, `site/releases/`, `site/checksums.txt` and
`site/latest.version` untouched. Check the page in a browser for console errors
and failed requests before merging.
