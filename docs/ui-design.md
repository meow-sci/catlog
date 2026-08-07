# catlog UI design specification

The shared contract for the redesign of catlog's **two** frontends:

| | `server/internal/web/` + `site/` | `spa/` |
|---|---|---|
| Rendering | Go `html/template`, server-rendered | Vite + React 19 + React Compiler, static |
| Interactivity | datastar (`/v1/feed/sse`) | nanostores + `EventSource` (`/v1/feed/stream`) |
| Styling | hand-written CSS in `site/assets/css/catlog.css` | Tailwind 4 (`spa/src/index.css`) |
| UI kit | none — semantic HTML | **react-aria-components (required)** |
| Owns | session, dashboard, credential wizard, docs | nothing stateful; read-only |
| Deployment | with `catlogd`, same origin | separate host, cross-origin, CDN |

They are a deliberate bake-off (`spa/README.md`, D14). They must reach **feature and
behaviour parity** on everything below. They must **not** reach markup parity — see §10.

> **Status.** §1 is *reported* from the repository. §2–§3, §5, §9–§11 are *proposed* — a
> specification to be implemented, not a description of what exists. §4 and §6–§8 describe
> **code that landed in `server/internal/` while this document was being written** and are
> therefore reports of a live contract: read the named Go files, not this summary, when the
> two disagree. Uncertainties are marked **⚠ FLAG**.

---

## Table of contents

0. [How to read this](#0-how-to-read-this)
1. [What is already true](#1-what-is-already-true)
2. [Design tokens](#2-design-tokens)
3. [Numerals](#3-numerals)
4. [The unit renderer](#4-the-unit-renderer)
5. [Information architecture](#5-information-architecture)
6. [What is shown, what is hidden, what is redacted](#6-what-is-shown-what-is-hidden-what-is-redacted)
7. [New features](#7-new-features)
8. [API status and remaining gaps](#8-api-status-and-remaining-gaps)
9. [Copy and tone](#9-copy-and-tone)
10. [Where the two frontends may legitimately differ](#10-where-the-two-frontends-may-legitimately-differ)
11. [What must not be thrown away](#11-what-must-not-be-thrown-away)
12. [Open questions](#12-open-questions)

---

## 0. How to read this

Two agents will implement this independently and must arrive at matching behaviour without
talking to each other. So:

- **Numbers here are normative.** `#2cfa1f` means `#2cfa1f`.
- **§4 is not negotiable and is not mine.** The unit rules are implemented in
  `server/internal/units` with a conformance table (`units.Conformance`) asserted by
  `units_test.go`. The SPA ports that table verbatim. Neither implementation may change a
  rule without changing the other and the table.
- **§11 is a do-not-break list.** The e2e suite asserts a DOM contract. A redesign that
  ignores §11 goes red and will not know why.
- Where this document and the repository disagree, the repository is the fact.

---

## 1. What is already true

### 1.1 Established before this survey

- **The "huge CSS" is not ours.** `site/assets/css/catlog.css` is 193 lines;
  `spa/src/index.css` is 48. The weight is the vendored `@picocss/pico` (2.1.1, ~83 kB) and
  its large default type scale. **Pico goes.** That means: drop the `@picocss/pico` entry
  from `vendorFiles` in `site/scripts/build.mjs`, drop the `<link>` from `layout.gohtml`,
  and grow `catlog.css` to carry the reset, the type scale, the theme and the form controls
  itself.
- **flexo's accent is `#2cfa1f`** (hover `#54fb4a`, press `#1fd615`, on-accent foreground
  `#052008`), full dark token set at `/Users/asherwin/repos/meow-sci/flexo/src/index.css`.
  We take the token *structure* — surface ladder → borders → foreground ladder → accent quad
  → semantic → wash — not only the green.
- **`user_key` appears nowhere in the public read API and in no template** except
  `docs_privacy.gohtml`'s prose, which explains the construction rather than printing a
  value. That is correct and this specification keeps it.
- **Both public frontends sit behind a cache.** Every read response carries
  `Cache-Control: public, s-maxage=30, stale-while-revalidate=300`, and so does every public
  HTML page (`web/pages.go`, `publicCache`). Consequence, which drives §7.1: **nothing
  personalised can ever be server-rendered on a public page.**
- **The two frontends disagree about number formatting today.** Go groups with U+202F
  (`templates.go`, `group()`); the SPA uses `Intl.NumberFormat('en')` — ASCII commas — and
  compacts above 100 000 to `4.2M`. §4 settles this.

### 1.2 Landed concurrently — read this before writing any code

A parallel work package added the following to `server/internal/` while this survey was
running. It supersedes several things I would otherwise have proposed, and in every case
its shape is the one to build against:

| File | What it is |
|---|---|
| `internal/units/units.go` | **The unit renderer**, in Go, with `units.Conformance` as the cross-language table. §4 |
| `internal/readapi/privacy.go` | `Redact` / `Label` — the install-derived-identifier redaction. §6.3 |
| `internal/readapi/events.go` | `GET /v1/players/{handle}/events` — the raw-event view. §6.2 |
| `internal/readapi/search.go` | `GET /v1/players?q=` — handle search. §7.2 |
| `internal/readapi/compare.go` | `GET /v1/compare?handles=` — N-handle comparison. §7.4 |
| `readapi/query.go`, `readapi.go` | `PlayerRow.Players`, `PlayerRow.Ascending`, redaction applied to every `context` |
| `directory/directory.go` | `Directory.Search` |
| `store/events.go` | `Events.PlayerEvents` — cursor-paged, newest-first, per player |

`cd server && go build ./...` is green with these in the tree.

**Two consequences for the frontends, and neither is optional:**

1. `web/templates.go` still registers the old `formatValue` as the `value` template
   function, and `contextPairs`/`scalar` still format context values with it. Both must move
   to `units.Format` and `units.ForKey`. `units.Format` takes the unit, so the template call
   becomes `{{value .Value $board.Unit}}`.
2. `web.Read` — the interface package `web` uses so that no page assembles its own rows —
   has only `BoardList`, `Board` and `Player`. The datastar site's new pages need
   `PlayerEvents`, `Search` and `Compare` added to it. **Do not reach around it into
   `store`**; the redaction and the ban filter both live behind it.

---

## 2. Design tokens

Both frontends define the same token names with the same values. The datastar site declares
them as CSS custom properties on `:root`; the SPA declares them in Tailwind's `@theme` so
they also generate utilities (`bg-panel`, `text-fg-muted`, …), as flexo does.

### 2.1 Colour

Two themes. The viewer's OS preference is the default; an explicit choice overrides and
persists. Neither is "the real one with the other bolted on".

```css
:root {                          /* light */
  --color-canvas:        #ffffff;
  --color-panel:         #f7f8f8;
  --color-panel-raised:  #ffffff;
  --color-panel-sunken:  #f0f1f2;
  --color-overlay:       #000000;   /* only ever with opacity */

  --color-border:        #dfe1e4;
  --color-border-strong: #c3c7cc;

  --color-fg:            #16181b;
  --color-fg-muted:      #5a6068;
  --color-fg-subtle:     #868c94;

  --color-accent:        #2cfa1f;   /* fills, chips, markers */
  --color-accent-hover:  #54fb4a;
  --color-accent-press:  #1fd615;
  --color-accent-fg:     #052008;   /* text/icons ON an accent fill, both themes */
  --color-accent-text:   #147a0d;   /* accent-COLOURED text, links, focus rings */

  --color-danger:        #b91c1c;
  --color-danger-fg:     #ffffff;
  --color-warning:       #8a6100;
  --color-warning-fg:    #ffffff;

  --color-wash-hover:    rgb(0 0 0 / 0.05);
  --color-wash-press:    rgb(0 0 0 / 0.09);
  --color-wash-selected: rgb(44 250 31 / 0.14);

  --shadow-panel:        0 1px 2px rgb(0 0 0 / 0.05);
  --shadow-popover:      0 12px 32px -12px rgb(0 0 0 / 0.25);
}
```

Dark — flexo's values, verbatim where they apply:

```css
--color-canvas:        #0b0c0e;
--color-panel:         #161719;
--color-panel-raised:  #1f2125;
--color-panel-sunken:  #0e0f11;

--color-border:        #2a2d32;
--color-border-strong: #3a3e45;

--color-fg:            #e8eaed;
--color-fg-muted:      #9aa0a8;
--color-fg-subtle:     #61666e;

--color-accent:        #2cfa1f;
--color-accent-hover:  #54fb4a;
--color-accent-press:  #1fd615;
--color-accent-fg:     #052008;
--color-accent-text:   #2cfa1f;   /* the raw green IS the text colour in dark */

--color-danger:        #ef4444;
--color-danger-fg:     #ffffff;
--color-warning:       #f5c542;
--color-warning-fg:    #1a1400;

--color-wash-hover:    rgb(255 255 255 / 0.06);
--color-wash-press:    rgb(255 255 255 / 0.10);
--color-wash-selected: rgb(44 250 31 / 0.12);

--shadow-panel:        none;      /* dark panels separate by border, not shadow */
--shadow-popover:      0 16px 48px -12px rgb(0 0 0 / 0.6);
```

Applied through all three selectors so the viewer's toggle wins in both directions:

```css
@media (prefers-color-scheme: dark) { :root { /* dark */ } }
:root[data-theme="dark"]  { /* dark */ }
:root[data-theme="light"] { /* light */ }
```

**Why `--color-accent-text` exists, and why you must use it.** `#2cfa1f` has a relative
luminance of ≈0.69. Against white that is **1.42:1** — not "a bit low", unreadable. Against
black it is ≈14.8:1. So in dark theme the raw green *is* the text colour; in light theme it
can only be a **fill** (with `--color-accent-fg` on top, ≈13:1). `--color-accent-text`
(`#147a0d`, ≈5.5:1 on white) carries every accent-coloured word, link and focus ring in
light and collapses to the raw green in dark. **Never write `color: var(--color-accent)`.**

Contrast, computed:

| pair | ratio | verdict |
|---|---:|---|
| `fg` on `canvas`, dark | ≈17:1 | fine |
| `fg-muted` on `canvas`, dark | ≈7.4:1 | fine |
| `fg-muted` on `canvas`, light | ≈6.4:1 | fine |
| `fg-subtle` on `canvas`, dark | ≈3.4:1 | **decoration and ≥24 px text only** |
| `accent-text` on `canvas`, light | ≈5.5:1 | fine |
| `accent` on `canvas`, light | ≈1.4:1 | **never for text or a ring** |
| `accent-fg` on `accent` | ≈13:1 | fine, both themes |

Nothing a reader needs may be `--color-fg-subtle`.

**Theme resolution.** `localStorage['catlog:theme']` ∈ `{light, dark, system}` →
`prefers-color-scheme` → dark. Stamp `data-theme="light|dark"` on `<html>` (absent for
`system`, so the media query wins). Both frontends ship a **synchronous inline `<head>`
script** that does this before first paint; anything async produces a white flash on a
dark-theme reload. `layout.gohtml` currently hardcodes `data-theme="dark"` for pico's
benefit — that attribute becomes ours and must become dynamic.
**⚠ FLAG** the site has no `Content-Security-Policy` today; if one is added this inline
script needs a nonce or hash, and it is the only inline script on the site.

`color-scheme: light dark` on `:root` so controls, scrollbars and the caret follow.

### 2.2 Type

**Anchored at 16 px, headings only slightly larger.** The loudest complaint about the current
UI is pico's type scale, where an `<h1>` is 2× body and an `<hgroup>` subtitle outsizes the
tables beneath it.

```css
--text-xs:   0.75rem;   /* 12px — timestamps, context pairs, footnotes */
--text-sm:   0.875rem;  /* 14px — dense cells, captions, chips */
--text-base: 1rem;      /* 16px — body, table cells, THE DEFAULT */
--text-lg:   1.125rem;  /* 18px — h3, panel titles, stat-tile values */
--text-xl:   1.25rem;   /* 20px — h2 */
--text-2xl:  1.5rem;    /* 24px — h1, the largest text on any page */

--leading-tight: 1.25;  --leading-snug: 1.4;  --leading-normal: 1.5;
--weight-normal: 400;   --weight-medium: 500; --weight-semibold: 600;
```

- **Nothing above 24 px anywhere.** The SPA's current `text-3xl` (30 px) home heading comes
  down.
- `h1` = `--text-2xl`/600/tight; `h2` = `--text-xl`/600; `h3` = `--text-lg`/600. Panel
  headers are `--text-sm`/600 uppercase `+0.04em` — a label, not a heading.
- Body copy, cells and inputs are `--text-base`. Never shrink a table to fit; scroll it (§11).
- Prose capped at `65ch`. Tables are not.
- Weights 400/500/600 only — Inter at 700 on dark blooms.
- Letter-spacing `-0.011em` on `--text-2xl`/`--text-xl`, `0` elsewhere, `+0.04em` on
  uppercase labels.

### 2.3 Spacing, radii, borders, elevation, motion

```css
--space-1: 0.125rem; --space-2: 0.25rem; --space-3: 0.5rem;  --space-4: 0.75rem;
--space-5: 1rem;     --space-6: 1.5rem;  --space-7: 2rem;    --space-8: 3rem;  --space-9: 4rem;

--radius-sm: 4px;    /* inputs, chips, row hover */
--radius-md: 6px;    /* buttons */
--radius-lg: 8px;    /* panels */
--radius-full: 9999px;

--border-width: 1px; --focus-width: 2px; --focus-offset: 2px;

--row-py: 0.5rem; --row-py-dense: 0.25rem; --row-px: 0.75rem; --panel-p: 1rem;
```

- **One container shape**: a panel — 1 px border, `--radius-lg`, `--color-panel`,
  `--shadow-panel`. The SPA already has it (`Panel` in `ui/kit.tsx`); the datastar site
  adopts it as `.panel`. No second card style.
- **Focus**: `outline: var(--focus-width) solid var(--color-accent-text);
  outline-offset: var(--focus-offset); border-radius: inherit;` on `:focus-visible` **and**
  `[data-focus-visible]` (React Aria drives the attribute). One ring for everything.
- **Motion**: exactly two — the feed-arrival flash (`catlog-arrive`, 1.2 s, accent at 22 % →
  transparent) and a 150 ms `transition-colors` on hover. Both wrapped in
  `@media (prefers-reduced-motion: reduce)`. Nothing else animates.
- **Hit targets**: ≥ 32 px on pointer, ≥ 44 px under `@media (pointer: coarse)`.

### 2.4 Inter, self-hosted from fontsource

Both frontends load **Inter Variable**, self-hosted, latin subset, **no CDN**. The datastar
site's build must stay hermetic (D2 — the same argument that made
`site/assets/vendor/datastar.js` a committed file).

```css
--font-sans: 'Inter Variable', Inter, ui-sans-serif, system-ui, -apple-system,
             'Segoe UI', Roboto, Helvetica, Arial, sans-serif;
--font-mono: ui-monospace, 'SF Mono', 'JetBrains Mono', 'Fira Code', Menlo,
             Consolas, monospace;
```

**Latin subset only, and it is safe rather than a gamble.** Every dynamic string catlog
renders is ASCII by construction: handles are `[a-z0-9._-]`; kitten names are "sanitized to
printable US-ASCII, max 32 chars" (`docs/events.md`); body names pass `statSuffix`'s
`[a-z0-9._-]` for boards and `sanitize()` for the feed. The only non-ASCII glyphs on any
page are ours — `—` U+2014, `×` U+00D7, `†` U+2020, `↑↓` U+2191/2193, `·` U+00B7,
`→` U+2192, and the U+202F group separator — and **every one falls inside the
Google-Fonts "latin" `unicode-range`** (`U+0000-00FF` plus `U+2000-206F`). U+202F in
particular is inside `U+2000-206F`, which matters because §4's separator is unrenderable
without it. Verify by rendering, not by trusting this paragraph.

**datastar site.** Add the fontsource package as a `devDependency` of `site/package.json`
and extend `vendorFiles` in `site/scripts/build.mjs`, resolving through `require.resolve` —
the file already does this for pico and says why ("so a version bump in package.json cannot
silently leave a stale path behind"). Copy the `.woff2` to `dist/fonts/` and declare
`@font-face` in `catlog.css` against `/static/fonts/…`, rather than copying the package's
own CSS, so the `src:` URLs match where the build actually put the files.
`font-display: swap`.

**SPA.** `pnpm add` the same package, `import` its latin CSS in `src/main.tsx` **before**
`./index.css`, set `--font-sans` in `@theme`. Vite fingerprints and rewrites the URLs.

> **⚠ FLAG — package name.** Almost certainly `@fontsource-variable/inter`, but I cannot
> resolve it offline and neither frontend has it installed. Resolve the real name, entry
> path and version at `pnpm add` time and **record it in `docs/DECISIONS.md`**, exactly as
> WP0 did for the datastar bundle. Both frontends must use the *same* package at the *same*
> major version; a metric or axis mismatch is invisible until the two screenshots are put
> side by side.

---

## 3. Numerals

Inter's tabular figures are why a leaderboard column reads as a column.

**The property is `font-variant-numeric: tabular-nums`. Never `font-feature-settings`.**
`font-feature-settings` replaces rather than composes: a descendant that sets it for any
other feature silently drops `tnum`, and the failure is invisible until a column
un-aligns. `font-variant-numeric` is the high-level property browsers map to `tnum` and it
composes. Tailwind's `tabular-nums` utility emits exactly this.

`spa/src/index.css` currently sets `font-feature-settings: 'tnum' 1` **on `body`**. Both
halves are wrong — the low-level property, and applying it globally including to prose —
and both are removed.

**Where it applies — the complete list:**

| Surface | |
|---|---|
| Any `<td>`/`<th>` or grid cell holding a number | ✅ |
| Rank cells and the `#` header | ✅ |
| Stat-tile values, quota counters (`3 / 5`) | ✅ |
| Timestamps and durations, `<time>` in the feed | ✅ |
| Pager counters (`ranks 51–100`), board populations | ✅ |
| Comparison-table cells | ✅ |
| Raw-event log columns (sim_t, recv) | ✅ |
| Feed **summary sentences** ("lithobraked at 214 m/s") | ❌ prose |
| Headings, body copy, docs, error text | ❌ prose |
| `<code>` (ULIDs, thumbprints) | ❌ already monospace |

```css
/* datastar site */
.tnum, td.value, td.rank, th.value, th.rank, .stat-value, time {
  font-variant-numeric: tabular-nums;
}
```

The SPA uses the `tabular-nums` utility on the same set of elements.

Additionally `font-variant-numeric: tabular-nums slashed-zero` on any ULID, career label or
key thumbprint rendered in the sans face. Inside `<code>` the mono font already
distinguishes `0`/`O`.

---

## 4. The unit renderer

**This is implemented and it is not mine.** `server/internal/units` is the authority; its
package comment is the specification and `units.Conformance` is the cross-language table.
Everything below restates it so a frontend implementer does not have to read Go — but
**read `units.go` anyway**, and when the two disagree, `units.go` wins.

### 4.1 The rules

`units.Format(v float64, unit string) string` returns **one string**, number and unit
together. There is no `{num, unit}` split — the string is the contract, and that is what
makes two implementations agree.

1. **Not finite** (`NaN`, `±Inf`) → `—` (em dash). Nothing in catlog may put a bare `NaN`
   on a public page.
2. **Three significant figures, trailing zeros trimmed.** Decimals =
   `clamp(2 - floor(log10 |x|), 0, 6)`. Round to that many, strip trailing zeros and any
   trailing `.`, group the integer part in threes with **U+202F NARROW NO-BREAK SPACE**.
   Zero renders `0`.
   **Rounding is defined on the magnitude** — `round(|x| · 10^d) / 10^d`, halves up, sign
   re-applied. This is spelled out precisely because `strconv.FormatFloat` and
   `Number.toFixed` disagree at ties, and `math.Round` / `Math.round` agree on a
   non-negative input. The TypeScript port must do the same dance, not call `toFixed`.
3. **Length (`m`), energy (`J`) and pressure (`Pa`) scale by SI prefix** — the largest of
   `1, k, M, G, T` whose scaled magnitude is ≥ 1. **No sub-unit prefixes**: `0.5 J` is
   `0.5 J`, never `500 mJ`.
4. **Speed (`m/s`) never scales.** `7 799 m/s` stays `7 799 m/s`. The reasoning, from the
   package comment and worth repeating because it is the rule most likely to be "improved"
   by mistake: m/s is the prompt a KSA player reads directly, every speed board is in m/s,
   and a per-value scale would put `7.8 km/s` and `998 m/s` in the same column.
5. **Time (`s`, `ms`) becomes a human duration**, largest two units that fit. Under a
   second → milliseconds. Under a minute → seconds at three significant figures. Above →
   two components, `5m 13s`, `1h 01m`, `243d 01h`, `1y 5d`; the trailing component is
   zero-padded to two digits **except** days inside a year. Whole seconds from a minute up —
   truncated, not rounded, so `1h 00m` never appears for something that has not reached an
   hour. A year is 365 days flat: this is a duration, not a calendar. `0` seconds → `0 s`.
6. **Anything else** — `g`, and the counter boards' labels (`RUDs`, `tumbles`, `bodies`, …)
   — is rule 2 followed by a space and the unit verbatim. An empty unit is rule 2 alone.
   This is what makes a board added later with a new label render `12 whatevers` rather
   than falling through to something that looks like a bug.

`units.ForKey(key string) string` maps a payload or `context` key to its unit, so a generic
renderer — a board's Detail column, the raw-event view — can format a blob it has no schema
for. It is suffix-driven, longest suffix first, and **deliberately total**: an unrecognised
key gets no unit rather than a wrong one.

> **The trap `ForKey` exists for:** `_ms` means **metres per second** in every §4.2 payload
> (`speed_ms`, `surface_speed_ms`, `fastest_ms`) while the board unit string `"ms"` means
> **milliseconds**. A renderer that keys on the suffix pattern without this table will show
> a 30 km/s ecliptic-frame roster speed as a 30-second duration. `_ms2` is matched before
> `_ms` for the same class of reason.

### 4.2 The conformance table

`units.Conformance` in full, as of the landed code. The SPA reproduces every row. `·` marks
U+202F.

| value | unit | renders |
|---:|---|---|
| `62` | `m/s` | `62 m/s` |
| `7799` | `m/s` | `7·799 m/s` |
| `37500` | `ms` | `37.5 s` |
| `48000000` | `J` | `48 MJ` |
| `2.1e7` | `s` | `243d 01h` |
| `0` | — | `0` |
| `0.002` | — | `0.002` |
| `0.5` | — | `0.5` |
| `4.25` | — | `4.25` |
| `62` | — | `62` |
| `214` | — | `214` |
| `7799` | — | `7·799` |
| `1234567` | — | `1·234·567` |
| `-214.4` | — | `-214` |
| `999` | `m` | `999 m` |
| `1500` | `m` | `1.5 km` |
| `1820000` | `m` | `1.82 Mm` |
| `1.5e9` | `m` | `1.5 Gm` |
| `4.2e12` | `m` | `4.2 Tm` |
| `0.5` | `m` | `0.5 m` |
| `2410` | `m/s` | `2·410 m/s` |
| `9.9e9` | `J` | `9.9 GJ` |
| `48750` | `Pa` | `48.8 kPa` |
| `101325` | `Pa` | `101 kPa` |
| `21000` | `Pa` | `21 kPa` |
| `0` | `s` | `0 s` |
| `0.45` | `s` | `450 ms` |
| `37.5` | `s` | `37.5 s` |
| `59.9` | `s` | `59.9 s` |
| `60` | `s` | `1m 00s` |
| `313` | `s` | `5m 13s` |
| `3661` | `s` | `1h 01m` |
| `86399` | `s` | `23h 59m` |
| `90000` | `s` | `1d 01h` |
| `31536000` | `s` | `1y 0d` |
| `31968000` | `s` | `1y 5d` |
| `312500` | `ms` | `5m 12s` |
| `-3661` | `s` | `-1h 01m` |
| `9.6` | `g` | `9.6 g` |
| `6` | `RUDs` | `6 RUDs` |
| `12` | `tumbles` | `12 tumbles` |
| `NaN` | `m` | `—` |
| `+Inf` | `RUDs` | `—` |

The five values this redesign was asked to pin are the first five rows. Note what they
demonstrate: **an orbital speed stays in m/s and gets grouped** (`7·799 m/s`), **a career
time becomes a duration** (`37.5 s`), **an impact energy takes an SI prefix** (`48 MJ`), and
**a transfer becomes a two-component duration** (`243d 01h`).

### 4.3 Where the renderer lives — the decision, and why it is right

**It is implemented twice — once in Go for the server-rendered site, once in TypeScript for
the SPA — and pinned by a shared conformance table. The JSON API publishes raw numbers and
never a formatted string.**

I reached this independently before finding `units.go`, and the package comment gives the
same argument, so it is settled from both ends. Restating it because it is the kind of
decision that gets re-litigated:

1. **A formatted string is not a number a client can sort.** The API's job is the value in
   the unit the event carried.
2. **It would freeze presentation into a CDN-cached public contract.** Every read response
   is `s-maxage=30`. A `value_display` field makes a decimal place an API change with a
   30-second-plus propagation delay.
3. **It would break the seam the SPA exists to test.** `spa/README.md`: "The only thing it
   shares with the rest of the repo is an HTTP contract." A frontend that renders the
   server's display strings is not a second implementation of anything, and the bake-off
   stops being a bake-off.
4. **The API is a public product.** `docs_api.gohtml`: "Everything on this site is also
   JSON. No key, no sign-up." A third party wants `7799`, in m/s.
5. **The risk is divergence, not duplication.** The function is ~120 lines. A shared vector
   table removes the divergence far more reliably than a shared implementation would remove
   the coupling.

**What the SPA implementer does:** port `units.Format` and `units.ForKey` to
`spa/src/ui/units.ts`, and port `units.Conformance` to a vitest that asserts every row.
`spa/src/ui/format.ts`'s `formatValue`/`exactValue` are replaced; `formatInstant`,
`formatAgo` and `$now` stay exactly as they are (§11).
**⚠ FLAG** — `units.Conformance` is a Go `var`, not a JSON file. The package comment notes
that a future `catlogctl` sub-command could emit it as JSON for the SPA to consume rather
than have it transcribed. Until that exists the table is copied by hand, and **a rule change
means editing three places in one commit**: `units.go`, `units_test.go`'s table, and the
SPA's port.

### 4.4 Rendering rules around the renderer

- **The unit is inside the string.** So the board table keeps the unit in the column header
  *as a label of what the column is* (`<th class="value">{{$board.Unit}}</th>`) while each
  cell still carries its own rendered unit. That is not redundant for the scaling
  quantities — a length column legitimately mixes `999 m` and `1.82 Mm` — and for counter
  boards it is mild repetition that buys column-independence on the profile, comparison and
  tile surfaces where there is no header. Prefer consistency over saving three characters.
- **Right-align every value cell**, `font-variant-numeric: tabular-nums`, `white-space:
  nowrap`. The existing CSS already does this; keep it.
- **`title` keeps the exact figure.** `title="7799 m/s"` on every formatted value. The SPA
  already does this via `exactValue`; the datastar site adopts it. It is also how a reader
  recovers the digits `48 MJ` hides.
- **`data-value`.** Every value cell carries `data-value="<the exact float, as sent>"`.
  This is not decoration: `site/e2e/boards.spec.ts` currently reconstructs numbers by
  stripping non-digits from rendered text, which breaks the moment a career-time board
  renders `5m 13s`. Move those assertions onto `data-value` in the same commit as the unit
  renderer. Not optional — it is the difference between 32/32 and a mystery.
- **Titlecase** for body names and RUD causes on `_ - .` boundaries, matching
  `stats.titleize` exactly: `luna` → `Luna`, `ground_impact` → `Ground Impact`. Both
  frontends implement it. Do **not** re-titlecase board titles — the server already did.
- **Instants are not `units.Format`.** They keep today's behaviour: fixed UTC, no locale.
  `2026-08-07 14:32 UTC` (Go) / `7 Aug 2026, 14:32 UTC` (SPA). Relative "4m ago" stays in
  the feed and the "updated" columns, computed from a store, never from `Date.now()` in
  render (§11).

---

## 5. Information architecture

### 5.1 Page inventory

● = both frontends. ○ = datastar only (needs a session, which the SPA has no concept of).

| Route | Both | What a human wants here |
|---|:--:|---|
| `/` | ● | **Where am I and what is happening?** Global tiles, three featured boards, the live feed, a search box, and — if a "me" handle is set — a personal card above the fold |
| `/boards` | ● | The index. Which boards exist, how populated, which way each reads |
| `/boards/{stat}` | ● | One board, paged, with a period selector, my row highlighted and reachable in one click |
| `/p/{handle}` | ● | One player: every placement, every rank *and its denominator*, and a way to start comparing |
| `/p/{handle}/events` | ● | **New.** The raw event log for that handle |
| `/compare?handles=a,b,c` | ● | **New.** Up to 8 players side by side across every board any of them is on |
| `/search?q=` | ● | **New.** Handle search as a real, linkable page, not only an overlay |
| `/login` | ○ | Choose an IdP; understand what it hands over |
| `/dashboard` | ○ | Handles, quotas, credentials, the wizard, account deletion |
| `/docs/{install,privacy,api}` | ○ | How to install; what is stored; how to call the API |
| 404 | ● | What was asked for, and two ways back |

The SPA's footer already says "Sign-in, handles and credentials live on the main catlog
site." Keep it; make it a link once a base URL is configurable.

### 5.2 The three journeys

**A — "see my own stats."** No account, no session. The user types their handle into search
(§7.2), lands on `/p/{handle}`, presses **This is me**. Thereafter, in both frontends:

- the header shows `You: whiskers_prime` linking to the profile;
- `/` leads with a **Your standing** panel — three best ranks, most recent activity, a link
  to the profile;
- every board table highlights their row (`--color-wash-selected` plus a left accent rule)
  and, when they are off-page, shows a sticky **You: #147** strip at the table foot linking
  to the page containing them;
- `/p/{handle}` says "This is you" rather than "This is me".

**B — "see global stats for all."** `/` opens with global tiles, then the featured boards,
then the feed. The **period selector** on `/boards/{stat}` —
`alltime | daily | weekly | monthly | yearly`, fully supported by the API since the rolling
periods landed and **used by neither frontend today** — turns a static ranking into "what
happened this week", which is the cheapest available way to make a leaderboard worth
revisiting. Shipping it is most of Journey B.

**C — "compare with friends."** From a profile, **Compare** adds that handle to a set held
in the URL. From search, a multi-select adds several. `/compare?handles=…` renders one row
per board any selected handle is on, one column per handle, best cell in each row marked
(respecting `ascending`), each with its world rank. Handles in the URL means the comparison
is a link you can paste into a Discord channel — the actual social act being designed for.

---

## 6. What is shown, what is hidden, what is redacted

Three different things; conflating them is how privacy bugs happen.

- **Hidden** — present in the API, not rendered by default. A display decision, made in the
  frontends.
- **Redacted** — removed or replaced *server-side* before any client sees it. A privacy
  decision. It can never be implemented in CSS or in a frontend.
- **Never collected** — the `user_key` posture. Unchanged.

### 6.1 Default tables — what is hidden

**Out of every default table, both frontends:**

| Field | Where it is | Why it goes |
|---|---|---|
| `context.flight` | five boards' context blob; rendered today by `contextPairs` | a client-minted ULID; means nothing to a reader and eats the widest column |
| `context.career` | the career-time boards | already **redacted** to a per-player label by the server (§6.3); still hidden here because a 16-character token is not a fact a reader wants in a table |
| `stat` key (`rud_ground_impact`) | SPA boards index and board subtitle | the title says it better; keep the key in `data-stat` and in the URL |
| event / session / batch ids | not rendered today | keep it that way |
| `updated_seq`, checkpoints | not rendered | internal |

**In, by default:** rank, handle, value+unit, the human-meaningful context (`body`, `from`,
`energy_j`, `t1_sim`), and when. That is the whole default row.

`contextPairs` (Go) and `describeContext` (TS) both become a **display allow-list**:

```
show:  body, from, energy_j, t1_sim
hide:  everything else, including any key not on the list
```

An unknown key is hidden rather than shown, so the fold layer can add context keys without
a frontend release and a new internal id cannot leak into a table by default. Values are
formatted with `units.ForKey` + `units.Format`, which is what turns
`energy_j: 48000000` into `48 MJ` and `t1_sim: 313` into `5m 13s`.

A **Details** disclosure on a board row and a profile row reveals the full blob as the API
sent it — which is already post-redaction, so there is nothing further to strip.

### 6.2 The raw event view

`GET /v1/players/{handle}/events` — **implemented**, `readapi/events.go`. Route
`/p/{handle}/events` in both frontends.

What it is for, in the endpoint's own words: every other endpoint publishes "a fold's
opinion about a history nobody outside the server can see. This is the history… what makes
'your record is 214 m/s' checkable rather than merely asserted."

Wire shape:

```jsonc
{ "handle": "whiskers_prime", "limit": 50, "type": "vehicle.orbit",  // type echoed if filtered
  "next": "41822",                                                    // opaque cursor, absent at the end
  "events": [
    { "id": "01J9V…",          // the envelope's client-minted ULID
      "type": "vehicle.orbit", "ver": 1,
      "session": "01J9V…", "flight": "01J9V…",   // flight absent on session/roster events
      "career": "b7k2q9x4m0nrt3vz",              // RELABELLED per player — see §6.3
      "sim_t": 1832.5,                           // absent, not zero, when the event carried none
      "recv": 1770000000123,                     // server receive time; wall_t is NOT published
      "payload": { … } } ] }                     // §4.2 payload, redacted, otherwise verbatim
```

Parameters: `limit` (default 50, max 200, clamped not rejected), `before` (a `seq` cursor;
negative is a 400), `type` (exact event-type filter, applied in Go over pages because
`ev_player` is `(player_id, seq)` and there is no type index).

**UI requirements:**

- Dense log, newest first, `--row-py-dense`. Columns: **when** (`recv`, fixed UTC),
  **type**, **sim_t** (through `units.Format(v, "s")`, so `1832.5` reads `30m 32s`), and the
  **payload** as pretty-printed JSON in a per-row disclosure.
- A type filter (multi-select → one `?type=` request per selection, or a single one and
  client-side narrowing; the API takes one type).
- **Page until `next` is absent, never until a page comes back short.** A filtered page that
  hit `maxEventScan` looks exactly like the end of the log and is not. The response comment
  says this explicitly; a client that gets it wrong silently truncates somebody's history.
- Payload numbers are shown **raw** — this is the view where a reader wants `7799`, not
  `7·799 m/s` — with the formatted form as a `title`. That is the inverse of the default
  tables, and deliberate.
- Unknown payload keys are rendered. §4.1 preserves them and "a raw view that dropped them
  would be lying about what catlog recorded."
- 404 for unknown, retired and banned identically — the same non-oracle answer as everywhere
  else.

**⚠ FLAG — flagged flights. This is the one place the landed code and a published promise
disagree.** `docs_privacy.gohtml` states: *"Flights flagged as cheated are stored and shown
to you, but score nothing and never appear publicly."* The shipped endpoint filters by
player and by type only — it does **not** exclude events of flagged flights — so shipping
the raw view as it stands makes that sentence false. Three ways out, and it is the owner's
call:

1. filter flagged flights out in `PlayerEvents` (matches the current promise; costs a
   `flight_state` lookup per page, in the other database file);
2. publish them and **amend the privacy sentence in the same commit**, in which case
   `EventRow` needs a `flagged` field so the UI can mark them — a client cannot currently
   tell, beyond noticing a `flight.flagged` event for the same flight elsewhere in the log;
3. publish them unmarked, which is the only option that is definitely wrong.

Until this is decided, **neither frontend should link the raw-event view from a public
page.** Build it, keep it reachable by URL, and gate the link.

### 6.3 What is redacted — the install hazard

> **`session.started`'s payload carries `install`, a per-install ULID, and `kid` and
> `career` are both derived from it. Publishing any of the three would link two handles
> belonging to the same person.**

`docs/events.md` gives the constructions:

```
career = crockford32_lower(SHA-256("catlog-career:" + install_id + ":" + save_key)[0..10])
kid    = crockford32_lower(SHA-256("catlog-kitten:" + install_id + ":" + roster_name)[0..10])
```

`install` is constant for the life of an installation and **independent of which handle is
shipping**. catlog explicitly permits one person to hold two accounts — two IdP subjects,
two handles, a documented quota — and the whole point is that no outside observer can tell.
A public `install` reduces "the handle is the only public identity" (Constitution §1) to
"the handle is the only public *label*", which is a much weaker claim and not the one the
privacy page makes.

The other two are the same hazard, narrowed: **`career` is identical whenever both accounts
play the same save** — the likely case when somebody claims a second handle and carries on
with their career — and **`kid` is identical whenever both fly a kitten of the same name**.

**`career` was already leaking.** `stats/boards.go` writes it into `context` on
`fastest_to_orbit` and every `fastest_to_<body>` board; `contextPairs` renders every context
key into a public table, and `/v1/leaderboards/{stat}` published it as JSON. That is fixed
by the landed code, described next.

**What the server now does** — `readapi/privacy.go`:

- **`install` and `install_id` are dropped.** Not blanked, not relabelled: dropped.
  Relabelling would produce a per-player constant that says nothing — one install per
  player, as far as a reader can see — while still being a token people would try to read
  meaning into. There is nothing to group by, so there is nothing to keep.
- **`career` and `kid` are relabelled per player** by
  `Label(playerID, kind, value) = crockford32(SHA-256("catlog-public-label:" + kind + ":" +
  playerID + ":" + value))[0..16]`. Both are grouping keys a reader genuinely wants — "these
  records came from the same save", "these EVAs were the same kitten" — and a per-player
  relabelling keeps that *inside* one handle while making two handles' labels unrelatable.
  The label has the same shape as the value it replaces (16 lowercase Crockford characters),
  so nothing downstream has to care, and it is stable for the life of a player id, so a
  client may cache it, group by it and put it in a URL.
- **Matching is by field name at any depth, not by event type.** §4.1 preserves unknown
  payload keys, so a future mod version can put a field anywhere; matching on the name covers
  `roster.snapshot.kittens[].kid` without enumerating it, and covers a new event type
  carrying `install` before anybody notices.
- **`wall_t` is omitted from `EventRow`** — the untrusted client clock, useless next to the
  server's receive time, and its offset from `recv` is a per-machine constant, which is a
  weak way to tell two accounts on one machine from two accounts on two.
- **Applied to `BoardRow.Context` and `PlayerRow.Context` too**, not only the raw view, with
  a fast path that skips the decode entirely when the blob mentions none of the trigger
  names — so the common board row costs nothing.

**This is better than the ordinal I was going to propose** (`career_no: 2`), for a reason
worth recording: an ordinal changes the field's shape and leaks the count of a player's
saves, while a same-shape label leaks neither and is still a usable grouping key.

**Two residuals the privacy file states rather than hides, and the UI must not pretend
otherwise:** kitten and vehicle **names** are the same across a person's two accounts if
they name things the same way — a soft correlator no redaction can remove without deleting
the content the view exists to show — and receive **times** correlate anything shipped at
the same moment, which the activity feed has published per handle since §5.6.

**Not hazards, so nobody over-redacts:** `flight` and `session` ULIDs are minted per flight
and per save-load, with no install in them; they are **hidden** from default tables for
clutter (§6.1) and **shown** in the raw view. Body names, kitten names and vehicle names are
public by design, sanitised, and a declared moderation surface.

**And the standing rule, restated because it is the one that must never bend:** `user_key`
never reaches a template, a JSON response, a log line or a `data-` attribute. The dashboard
is the only page whose data carries it (`identity.DashboardData.Me.Sub`) and it renders
`.Me.IdP`, the quotas and the handle list — never `.Me.Sub`. A dashboard redesign must
preserve that; `web/web.go`'s "What may never appear on a page" comment is the note that
says so.

---

## 7. New features

### 7.1 The "me" handle in localStorage

**Client-side in both frontends, and that is forced rather than chosen.** Every public page
is served `s-maxage=30` to a shared cache, so there is no server-rendered personalisation
available to either frontend. Both do the same thing in the same place.

**Storage.** Key `catlog:me`, value the handle as a plain string in display casing. One key,
no JSON envelope, so a user can read and clear it. (Storage does not cross origins, so the
two frontends do not actually share a value; the same key name means a future same-origin
deployment would.)

**Setting it:** a **This is me** toggle on `/p/{handle}`; a `You: <handle>` header chip with
a clear control; and — datastar only — an offer after the wizard's step 4, the one moment
the site knows the handle for certain.

**Effects, identical in both:** header identity, the `/` **Your standing** panel, row
highlighting on every board table, and the sticky **You: #147** strip when the row is
off-page (§5.2).

**When it is set but the handle no longer resolves.** `/v1/players/{handle}` answers 404
identically for unknown, retired and banned — deliberately, so it is not a ban oracle. So
the UI:

1. **Never guesses.** *"catlog has no public profile for `whiskers_prime` any more."* Not
   banned, not deleted, not retired, not renamed. Repeating the API's own silence is the
   honest behaviour and the correct one.
2. **Never auto-clears.** The stored value is the user's data; a 404 during an incident, a
   rebuild, or a moderation action that gets reversed must not silently erase it. The notice
   offers **Keep it** (dismiss for this session) and **Forget it** (clear the key).
3. **Distinguishes a 404 from a failure.** `status === 0` — offline, DNS, a refused CORS
   preflight — shows nothing at all. Only a real 404 raises the notice. The SPA's
   `ApiError.notFound` getter already encodes this and is the only place it should live.
4. **Degrades to nothing.** Personalised panels are absent, not empty-with-a-spinner.

**Privacy properties, to be stated in `docs/privacy` when this ships:** the "me" handle is a
local browser preference. It is never sent to catlog as an identifier, never appears in a
query string on a cached URL, never in a `Referer`, and is not a login. Two people on one
machine share it, exactly like a bookmark.

**API needs:** none. `/v1/players/{handle}` exists.

### 7.2 Handle search

`GET /v1/players?q=` — **implemented**, `readapi/search.go`. Note the route: it is
`/v1/players`, not `/v1/handles`.

```jsonc
// GET /v1/players?q=whisk&limit=20
{ "query": "whisk", "limit": 20,
  "handles": ["whiskers_prime", "whiskey_ace"],   // bare strings, nothing else
  "truncated": true }                             // more matched than limit allowed
```

Behaviour the UI must respect, all of it load-bearing:

- **Matching is prefix-first, then substring**, each group lexicographic by the lowercase
  handle, display casing preserved. So the empty state says *"No handles match `xyz`."* —
  **not** "start with".
- **`MinQueryLen` is 2, and a shorter query is a `400`, not an empty `200`.** The UI must
  **not fire a request below two characters**, and must not surface an error for a one-character
  input — just no suggestions yet. Getting this wrong turns every search box into a 400 on
  the first keystroke.
- `MaxQueryLen` is 150 (a handle's own cap); longer is also a 400. Truncate client-side.
- `limit` default 20, max 50, clamped.
- **`truncated` means "narrow your query", not "load more".** There is deliberately **no
  offset**: "a paged search over a live directory is a promise this cannot keep." Render
  *"More handles match. Try a longer query."* and no pager.
- Banned and retired handles are absent by construction — the endpoint scans
  `directory.Directory`, which is the same map every board page resolves through and which
  already excludes them. Do not add a filter; there is nothing to filter.

**UI.** A search input in the header on every page, plus a real `/search?q=` route so a
search is linkable. Typing shows suggestions (250 ms debounce, `AbortController` per
keystroke); Enter goes to the results page; a single exact match on Enter goes straight to
that profile.

- SPA: React Aria `ComboBox`, `allowsCustomValue`, `menuTrigger="input"`. Arrow keys and
  Escape come free, which is the whole argument for React Aria here.
- datastar: an ordinary `<form action="/search" method="get">` that works with JavaScript
  off, enhanced by `data-on-input__debounce.250ms="@get('/search/suggest?q=…')"` patching a
  `<ul id="search-suggest">`. The server renders `/search?q=` itself, cacheable by URL.

### 7.3 Per-handle board ranks

`PlayerRow` now carries everything needed. Two fields landed for exactly this:

- **`players`** — how many players hold a value on the board, so `#3` becomes **`#3 of 41`**.
  It counts rows **including banned players**, like `BoardSummary.Count` and for the same
  reason. **Rank is ban-filtered and the denominator is not, so a rank can be better than
  the denominator implies, never worse.** Never compute a percentile that could exceed 100 %;
  clamp it.
- **`ascending`** — repeated on the profile row so a client can render direction without
  fetching the board index. "#1 with the lowest number" is unreadable without it.

**UI.** Each profile row shows `#3 of 41` and a thin percentile bar. Top 3 gets
`--color-accent-text`; top 10 % gets `--color-fg`; the rest `--color-fg-muted`. The row
links to the board **at the page containing that rank** —
`offset = floor((rank - 1) / PAGE_SIZE) * PAGE_SIZE` — so "see where I sit" is one click
and needs no new endpoint.

**"Near me" rows** — three above and three below on a board page — are computed client-side:
the client knows the rank, so it requests `offset = max(0, rank - 1 - 3)`. No API change. It
can be off by one or two if a ban lands between the two requests; that is acceptable and
must not be papered over with a second endpoint.

**Period-scoped ranks** are the one thing still missing. §8.

### 7.4 N-handle side-by-side comparison

`GET /v1/compare?handles=a,b,c` — **implemented**, `readapi/compare.go`.

**I had specified a client-side join over N profile documents, and I was wrong.** The
endpoint's argument is better and I am recording it rather than burying it: N client-side
requests "means N profile requests whose answers can disagree — a projection commit between
the first and the last shows one player's new record next to another's stale rank. One
request reads them all against one view of the projections, so the table a reader sees is
internally consistent." It is also literally the profile endpoint pivoted board-first, which
keeps the ban-discounting rank arithmetic and the redaction as one implementation each.

```jsonc
{ "handles": [ { "handle": "whiskers_prime", "found": true, "since": 1770000000000 },
               { "handle": "ghost",          "found": false } ],
  "boards":  [ { "stat": "rud_total", "title": "Rapid Unscheduled Disassemblies",
                 "unit": "RUDs", "ascending": false, "players": 41,
                 "rows": [ { "handle": "whiskers_prime", "value": 6, "rank": 3,
                             "context": null, "updated": 1770000000123 } ] } ] }
```

Behaviour the UI must respect:

- **Up to 8 handles** (`MaxCompareHandles`). Extras are **dropped, not rejected**, and the
  effective list is echoed in `handles` — so the UI renders what it got and, if it asked for
  more, says so. Cap the picker at 8 to match rather than letting the server do it silently.
- **`found: false` is a column, not an omission.** Render it headed with the requested
  string and a muted *"no such player"*. Silently dropping it lets a typo look like a defeat.
  It says no more than asking for that one profile already says.
- **A board only some of them are on lists only the rows that exist.** An absent player is
  **absent, not zero** — same rule the folds follow for a missing `peak_g`. Render the gap
  as `—` with `title="not on this board"`.
- **`rank` is the world rank, not the rank among the compared handles** — "3rd in the
  world", not "2nd of your friends". Label it so.
- **The best cell in each row is decided by `ascending`**, never inferred. Mark it with
  `--color-accent-text` and an `aria-label` suffix.
- Board order is the board-index display order, so the table is stable as handles are added.
- Repeated `?handles=` parameters are accepted (`a,b&handles=c` is one request); an empty
  list is a valid empty comparison, not an error — which is what a UI with nobody selected
  yet should request.
- `min_players` does **not** apply here, as on a profile: a board somebody is actually on is
  shown whether or not the public index lists it.

**UI.** SPA: React Aria `TagGroup` of removable handle chips plus a `ComboBox` to add.
datastar: a `<form>` with the same chips as removable `<a>`s and a search field, rendered
server-side from the query string. Both write `?handles=` on every change so the URL is
always the shareable truth.

---

## 8. API status and remaining gaps

Most of what §5–§7 needs **has landed** (§1.2). What remains:

**1. Period-scoped profiles and ranks — still missing, and it blocks real UI.**
`readapi.Server.player` reads `player_stat` only. Without it: no "how did I do this week" on
a profile, no windowed comparison, and Journey B is half-shipped — the board pages can offer
`?period=` because `Board` already supports it, but a profile cannot.
Proposed shape, mirroring `BoardResponse`, which already does exactly this:

```jsonc
{ "handle": "…", "since": 0,
  "period": "weekly", "bucket": "2026-W32",   // echoed; bucket absent for alltime
  "stats": [ /* PlayerRow, from player_stat_period */ ] }
```

**⚠ FLAG** — the rank arithmetic is the hard half. `Projections.StatAhead` queries
`player_stat`; a period-scoped rank needs the `player_stat_period` equivalent, and the same
`StatsForPlayers` ban-discount over that table. I do not know whether that is planned. If it
is not, the honest interim is `?period=` returning **values without ranks**, and the UI
hiding the rank column for a windowed view rather than showing an all-time rank next to a
weekly value — which would be a wrong number, not a missing one.

**2. `count` on `BoardResponse` — nice to have.** Both frontends currently infer "there is
probably more" from `rows.length >= limit` (`BoardPage.tsx` says so in a comment). The board
census is already computed for the index and `PlayerRow.Players` now carries the same figure
per board, so the number exists; it is just not on the board response. With it, the pager
becomes real and `/boards/{stat}` can say "41 players". Without it, keep the current
inference — it is honest, and §4.8 publishes no total by design.

**3. Global stat tiles for `/` (Journey B) have no source.** `/admin/stats` has some of the
numbers and is loopback-only (§5.9). Two options: a small public `GET /v1/stats` summary, or
assemble the tiles from `/v1/leaderboards` — which answers "how many players are on each
board" rather than "how many events exist". **The second needs no API work and is what I
would ship first**; treat a public `/v1/stats` as a later question, and note that it is the
one endpoint here whose cost is not obviously bounded.

**4. `EventRow.flagged`** — needed only if §6.2's flagged-flight question resolves toward
publishing them. See §12.

**Not needed, recorded so nobody builds them:**

- A `quantity` field on board metadata. I had proposed one so no frontend string-matches
  `"m/s"` — `units.Format` takes the unit string directly and switches on it, so the
  classification already lives in one place. Dropped.
- `?around=<handle>` on a board — the client computes the offset from the rank it has (§7.3).
- Any endpoint answering "is this handle me". There is no session on the read API and there
  must not be. `credentials: 'omit'` in the SPA client and the absence of
  `Access-Control-Allow-Credentials` in `readapi/cors.go` are load-bearing and stay.

---

## 9. Copy and tone

The existing whimsy is liked, is genuinely good, and is **kept**. Extending it needs rules,
because the failure mode of "be funny" in a UI is a joke where a fact was needed.

### 9.1 Exemplars — keep these words

Voice:

- *"Everything you broke, ranked"* — the home `h1`.
- *"catlog watches your Kitten Space Agency flights and keeps score. Lithobrakes you walked
  away from, speeds nobody should reach, and every rapid unscheduled disassembly along the
  way."*
- *"Leaderboards for things that went wrong"* — the SPA's `h1`.
- *"Rapid Unscheduled Disassemblies"* as a board title; *"an unexplained disassembly"* for
  an unknown cause.

Empty states — the best place for whimsy, and currently the best writing on the site:

- *"Nothing has happened yet. Fly something."*
- *"Nobody has scored here yet."* / *"Nobody is on this board yet."*
- *"Nothing here"* as a 404 heading.
- *"There is nothing on this page — try going back."*
- *"This player is not on any board yet."*

Feed lines (`stats/feed.go`) — the model for turning a fact into a sentence:

- *"demo_ace lithobraked at 214 m/s on duna — and survived"*
- *"demo_ace brought 3 kittens home safely"*
- *"demo_ace said goodbye to kitten Mittens"*
- *"demo_ace made orbit around luna (120 km × 118 km)"*

Explanations that earn their length:

- *"A board with no entries is still a board."*
- *"a leaderboard with a single entrant is not a leaderboard"*
- *"Ranks skip nobody: a banned account is removed from the board rather than leaving a hole
  in the numbering."*
- *"catlog would rather record the truth than pretend it did not happen."*
- *"It is permanent: catlog has no rename, and a handle is never recycled to anyone else."*
- *"Signed in with discord since 2026-01-01. catlog knows nothing else about you."*
- *"Five minutes, one file, no account linking."*

### 9.2 Rules a new writer can follow

1. **Dry and understated, affectionate about failure.** catlog is a record of things going
   wrong, told without pity and without gloating. The joke is the subject matter; the
   sentence stays deadpan.
2. **Never invent a fact for a joke.** The API answers 404 for unknown, retired and banned
   identically. The copy says *"catlog has no public profile for X."* and stops. A funnier
   line that implies a ban is a lie.
3. **British spelling**, matching the repository's prose (*localising*, *normalisation*,
   *behaviour*, *apologise*).
4. **Sentence case for everything we write.** Board titles are title case because the server
   generates them (`stats.titleize`); do not re-case them and do not imitate them.
5. **No exclamation marks. No emoji. No "we".** catlog is one person's hobby server; when it
   must speak it says "catlog".
6. **A number is never a joke.** Units, ranks, counts and timestamps are rendered by §4 and
   surrounded by as few words as possible.
7. **Empty states get the whimsy; loading states get nothing.** *"Loading boards…"* is
   correct and complete. A spinner with a joke is a spinner you read twice.
8. **One sentence, or one sentence plus one that says why.** The `min_players` paragraph is
   the longest thing on the site and it is long because it answers a real support question
   end to end. That is the ceiling, not the target.

### 9.3 Where whimsy is wrong — four places, no exceptions

**Errors the user must act on.** The auth-error page is a code, a detail and a way back —
deliberately plain, and the e2e suite asserts its structure. The wizard's failures name the
error code. The SPA's `Failure` shows the server's own `detail` plus the status, because
*"when this is a CORS refusal or a stopped server, the difference between 'failed to fetch'
and a 500 is the entire diagnosis"*. Never replace a diagnosable message with a generic
apology, funny or otherwise. Search's 400s (§7.2) belong here too — but the right fix is not
to render them, it is not to send the request.

**Privacy and identity statements.** Load-bearing claims about what the system *cannot* do,
where the precision is the point:

- *"catlog never receives your email address"* — immediately followed by *"This is not a
  promise about what we do with your email. It is a statement about what catlog is able to
  know."*
- *"The private key never leaves your browser — catlog only ever receives the public half,
  so it cannot send you a second copy of this file."*
- *"catlog will not accept your private key even by accident."*

Never soften, shorten or make one of these playful. §6.3's redactions need a paragraph in
this register when the raw view ships — plain, specific, stating the mechanism rather than
the intention. A model, matching the existing voice:

> catlog records an installation id with your session events. It is never published — it
> would link two handles belonging to the same person. Career and kitten identifiers are
> relabelled per player for the same reason: they still group your own records together, and
> they cannot be matched against anybody else's.

**Destructive confirmations.** *"This deletes every event, batch and credential, and retires
your handles permanently — neither you nor anybody else can ever claim them again. It cannot
be undone."* Consequences, in order, no jokes.

**Numbers, units and ranks.** §4. A unit is never abbreviated for a pun.

---

## 10. Where the two frontends may legitimately differ

Parity is about **features and behaviour**, not markup. Every row below is sanctioned;
nothing else is.

| | datastar site | SPA |
|---|---|---|
| **UI kit** | none. Semantic HTML, hand-written CSS, no component library. Staying lean *is* its half of the bake-off | **react-aria-components, required.** `Table` for boards, `ComboBox` for search, `TagGroup` for comparison chips, `Tabs` for periods, `Dialog`/`Popover` for details and raw payloads, `ToggleButton` for the theme |
| **Client JS** | `datastar.js` (vendored, 34 kB), `keygen.js`, the inline theme script, one small `me.js`. That is the whole budget | the bundle |
| **Personalisation** | client-side: `me.js` stamps `data-me="<handle>"` on `<html>` and CSS does the rest (`tr[data-handle="…"]`). Forced by `s-maxage=30` | client-side: a nanostores atom over `localStorage` |
| **Comparison** | server-rendered at `/compare?handles=…` via a new `Compare` method on `web.Read`, cacheable by URL, no client JS | fetch `/v1/compare`, render with React Aria |
| **Search** | server-rendered `/search?q=`, progressively enhanced with a debounced datastar suggestion patch | React Aria `ComboBox` against `/v1/players?q=` |
| **Raw events** | server-rendered `/p/{handle}/events` via `web.Read.PlayerEvents` | fetch `/v1/players/{handle}/events` |
| **Live feed** | `/v1/feed/sse`, HTML `PatchElements` frames, zero client code | `/v1/feed/stream`, JSON, nanostores + `EventSource` |
| **Account surface** | owns `/login`, `/dashboard`, the wizard, `/docs/*` | has none, and links out |
| **Routing** | server routes, full page loads | History API, base-path aware, `404.html` fallback |
| **Styling** | CSS custom properties + hand-written rules | the same tokens in Tailwind `@theme`, consumed as utilities |
| **Timestamps** | `2026-08-07 14:32 UTC` | `7 Aug 2026, 14:32 UTC` — two renderings of the same fixed UTC instant, both locale-independent |

**What may *not* differ:** which pages exist, what each shows, what is hidden, every
number's formatting (§4 — character for character), the colour and type tokens (§2), the
copy for shared surfaces, the "me" semantics including its failure behaviour (§7.1), the
comparison rules (≤8 handles, `found: false` as a column, best-cell by `ascending`, absent ≠
zero), the search rules (no request under 2 characters, `truncated` means narrow not page),
and the tone rules (§9).

---

## 11. What must not be thrown away

Things a redesign will delete without noticing, and what breaks when it does.

**The DOM contract the e2e suite asserts (32/32 depends on it).** `#home-title`;
`#featured-boards .featured-board[data-stat]`; `tr.board-row[data-rank][data-handle]` with
`td.value` and `td.context`; `#boards-index tr.boards-row[data-stat]`; `#boards-title`;
`#boards-note` (must contain the server's `min_players` number); `#board-title[data-stat]`;
`#board-direction[data-ascending]`; `#profile-handle[data-handle]`;
`#profile-stats tr[data-stat][data-rank]`; `#feed-panel`; `#feed[data-source][data-count]`;
`li.feed-item[data-feed-id][data-type]`; `#not-found`, `#not-found-detail`,
`#not-found-home`; `#auth-error[data-error]` with `#auth-error-code`, `#auth-error-detail`,
`#auth-error-retry`; `#docs-title`; `#privacy-no-email`; `#privacy-scopes`;
`#docs-api-endpoints`; the whole `#wizard-*` set; `#quota-handles`, `#quota-issuances`,
`#quota-ttl`; `.credential[data-jkt]`; `#logout`; `#delete-account`. Rename any of these and
update the spec file in the same commit, never after.

**`feed-list` and `feed-item` being one partial, used by both the page and the SSE
handler.** A line patched in over the wire is byte-identical to one rendered into the page
because they come from the same template. Inlining feed markup into `home.gohtml` breaks the
live feed in a way that looks like a datastar bug.

**`data-source="ssr|sse"`.** The only signal distinguishing an open stream from a page whose
datastar module never ran — both show the same rows. `feed.spec.ts` waits on it.

**`feedItemID` shared between `feed.go` and the template.** The SSE handler removes
scrolled-off lines by this id.

**`ascending`, published per board and never inferred.** Both frontends state it in words
("Lowest wins.") and both mark it. Drop it and `fastest_to_orbit` is presented backwards.
It is now on `PlayerRow` too, so the profile has no excuse either.

**The `rewound` dagger and its exact tooltip** — *"An earlier save of this career was loaded,
so its clock did not only run forwards."* It qualifies a number and does nothing else; the
row ranks normally. Keep it in both, with the `sr-only` "(career rewound)".

**The `min_players` paragraph** and **"Ranks skip nobody"**. Each answers a support question
end to end. They look like padding and are not.

**`pickFeatured` (SPA) and `handleHome`'s skip-if-missing (Go).** The board index is
assembled from data; a front page pinned to three literal names can be pinned to a board
that is not there. Both handle it, differently, deliberately.

**Fixed UTC everywhere.** *"a leaderboard is a shared artefact, and localising it would make
two people describing the same row disagree."* A redesign that adds a friendly local
timestamp breaks a stated property.

**The wizard's markup discipline.** Every step is in the DOM from the start and toggled with
`[hidden]`, so `keygen.js` never builds HTML from strings — "the only HTML the browser gets
is the HTML the server sent". A componentised wizard violates the one invariant that file
exists to protect.

**`Failure` showing the server's `detail` and status.** See §9.3.

**`credentials: 'omit'`** in the SPA client, and `readapi/cors.go` emitting no
`Access-Control-Allow-Credentials`, ever.

**`$now` as a store rather than `Date.now()` in render.** React Compiler memoization assumes
render is idempotent; `format.ts` is the file that makes it true. Replacing `formatValue`
with the unit renderer must not disturb `$now`, `formatAgo` or `formatInstant`.

**`withoutHandle()`** in the SPA feed — the server composes complete sentences for the
server-rendered panel, and the SPA renders the handle as its own link, so the prefix would
otherwise appear twice.

**Focus and scroll management in the SPA** — `main` with `tabIndex={-1}`, focus moved on
route change, `history.scrollRestoration = 'manual'`. Small, correct, easy to delete.

**Accessibility already present:** `<output>` for loading, `role="alert"` for failures,
`aria-live="polite" aria-relevant="additions"` on the feed, `aria-current="page"` on nav,
`scope="col"` on every header cell, `sr-only` direction text, `isRowHeader` on the handle
column, `aria-disabled` spans instead of dead pager links.

**`prefers-reduced-motion` on the feed arrival flash.**

**Tables scrolling inside themselves** (`display: block; overflow-x: auto`) rather than
making the page scroll horizontally.

**`#feed-heartbeat`** — the hidden element the 20 s patch writes to, which keeps nginx and
any CDN from dropping an idle stream.

**The em dash for a value that is not a number.** Both frontends already do it, and
`units.Format` does it now. Never `NaN`, never `0`, never blank.

---

## 12. Open questions

1. **Flagged flights in the raw-event view (§6.2).** The landed endpoint publishes them;
   `docs/privacy` says they never appear publicly. This is a live contradiction and it
   needs an owner decision before the raw view is linked from any public page. It is the
   only item here that is a correctness problem rather than a preference.
2. **Period-scoped profile ranks (§8.1).** Needs a period-aware `StatAhead`. Until it
   exists, a windowed profile must hide ranks rather than show all-time ranks beside
   weekly values.
3. **The fontsource package name and version (§2.4).** Resolve at install time, record in
   `docs/DECISIONS.md`, identical package in both frontends.
4. **`units.Conformance` as JSON (§4.3).** Today the table is transcribed into the SPA by
   hand, so a rule change touches three places. A `catlogctl` sub-command emitting it would
   remove the transcription; the package comment already contemplates it.
5. **`count` on `BoardResponse` (§8.2).** Cheap, and it turns an inferred pager into a real
   one. API agent's call whether it is in scope.
6. **Global stat tiles (§8.3).** Ship the version assembled from `/v1/leaderboards` first; a
   public `/v1/stats` is a separate question with a less obviously bounded cost.
7. **`web.Read` growth.** The datastar site needs `PlayerEvents`, `Search` and `Compare` on
   that interface. Straightforward, but it is the seam that keeps the ban filter and the
   redaction in one place, so it must be widened deliberately rather than bypassed.
