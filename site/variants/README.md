# Blueprint website concepts

Three static landing-page previews preserve the existing white background and
blue accent (`#2b5cd9`):

- `signal.html`: centered headline and a wide animated agent network.
- `orbit.html`: split introduction with an orbital agent network.
- `blueprint.html`: drafting surface and a full-width system diagram.

Open `index.html` to compare them. No build step, dependencies, external fonts,
or runtime services are required. `style.css` and `motion.js` are shared;
the PNG files are gallery previews. Networks and terminal output are illustrative.
Install instructions use the GitHub release installer with `--local`.

Serve these nine files together under `/variants/` without replacing the main
page: four HTML files, `style.css`, `motion.js`, and the three PNG files.

Verified in Chromium at 1440px desktop and 390px mobile: all gallery images load,
no horizontal overflow or JavaScript errors, agent selection and terminal tabs
work. Animation supports pause and reduced motion and stops offscreen or in a
hidden tab. Clipboard failure shows manual-copy instructions.
