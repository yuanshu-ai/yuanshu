# Yuanshu brand assets

This directory is the canonical runtime source for the selected **Remote hub** mark.

- `yuanshu-mark-primary.svg`: full-canvas primary mark for light backgrounds.
- `yuanshu-mark-on-dark.svg`: full-canvas mark for dark backgrounds.
- `yuanshu-mark-monochrome.svg`: one-color master.
- `*-compact.svg`: the same geometry with a tighter viewBox for small UI surfaces.
- `yuanshu-app-icon.svg` and `yuanshu-app-icon-512.png`: application and browser icon tile.
- `yuanshu-menubar-template-18@2x.png`: macOS template-image source.
- `yuanshu-tray.ico`: Windows notification-area icon.

Run `pnpm brand:sync` after changing a canonical asset. `pnpm brand:check` verifies that Web, Node Web, pairing, tray, and README copies match this directory.

Do not redraw individual product surfaces independently. Shape changes require updating the brand direction in the separate `docs` repository first.
