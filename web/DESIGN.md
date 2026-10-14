---
name: piway
colors:
  light:
    background: "#ffffff"
    foreground: "#000000"
    surface-muted: "#f5f5f5"
    surface-hover: "#ededed"
    text-muted: "#666666"
    border: "#e5e5e5"
    border-input: "#8a8a8a"
    primary: "#000000"
    on-primary: "#ffffff"
    brand: "#cd2355"
    on-brand: "#ffffff"
    brand-text: "#cd2355"
    destructive: "#cd2355"
    ring: "#cd2355"
  dark:
    background: "#000000"
    foreground: "#ffffff"
    card: "#0f0f0f"
    surface-muted: "#171717"
    surface-hover: "#212121"
    text-muted: "#a3a3a3"
    border: "#262626"
    border-input: "#6b6b6b"
    primary: "#ffffff"
    on-primary: "#000000"
    brand: "#cd2355"
    on-brand: "#ffffff"
    brand-text: "#e5476f"
    destructive: "#e5476f"
    ring: "#e5476f"
  chart:
    light: ["#cd2355", "#262626"]
    dark: ["#e5476f", "#d4d4d4"]
typography:
  sans: "IBM Plex Sans (variable)"
  mono: "IBM Plex Mono 400/500"
rounded: 0
---

# piway design system

The dashboard of a Raspberry Pi that runs your apps. It is an instrument panel
first: numbers you glance at, states you must not miss, actions you take
rarely and deliberately.

The implementation lives in `src/index.css` (tokens) and
`src/components/ui/*` (shadcn/ui components restyled through their `cva`
variants). The rendered reference is the design system page at
`/design-system`. When this file and the code disagree, fix one of them in the
same change.

## Principles

1. **Ink first, one signal.** The interface is black, white and neutral gray.
   Raspberry is the only hue, and it means *look here*: the brand mark, the focus
   ring, the selected item, the live line in a chart, a critical state. If
   raspberry appears where nothing needs attention, it is wrong.
2. **Sharp.** Radius is 0 on everything: buttons, inputs, cards, badges, menus,
   dialogs. Structure comes from hairlines and spacing, not shadows.
3. **Numbers are the content.** Metrics use tabular figures so digits do not
   jump while values update every second. Units are smaller and muted.
4. **Every state is visible without color.** Status always pairs color with an
   icon and a word. Focus is a 2px outline. Disabled is reduced opacity plus
   `not-allowed`.
5. **Calm motion.** Data updates in place without animation. Motion is used only
   to answer a user action (open, close, expand), stays under 200ms, and is
   removed under `prefers-reduced-motion`.

## Color

| Role | Token | Light | Dark | Use |
| ---- | ----- | ----- | ---- | --- |
| Canvas | `background` / `foreground` | `#ffffff` / `#000000` | `#000000` / `#ffffff` | Page and body text |
| Card | `card` | `#ffffff` | `#0f0f0f` | Panels; separated by a `border` hairline |
| Muted surface | `muted` | `#f5f5f5` | `#171717` | Table headers, code, empty states |
| Hover wash | `accent` | `#ededed` | `#212121` | Ghost hover, menu highlight |
| Secondary text | `muted-foreground` | `#666666` | `#a3a3a3` | Units, captions, descriptions |
| Primary action | `primary` / `primary-foreground` | `#000000` / `#ffffff` | `#ffffff` / `#000000` | The one main action in a group |
| Brand | `brand` / `brand-foreground` | `#cd2355` / `#ffffff` | same | Brand mark, a rare emphasised action, selection fill |
| Brand text | `brand-text` | `#cd2355` | `#e5476f` | Links to attention, the live indicator |
| Critical | `destructive` | `#cd2355` | `#e5476f` | Errors, critical states, destructive actions |
| Hairline | `border` | `#e5e5e5` | `#262626` | Structural lines (decorative, no contrast target) |
| Field border | `input` | `#8a8a8a` | `#6b6b6b` | Input and select outlines (≥ 3:1 for UI) |
| Focus | `ring` | `#cd2355` | `#e5476f` | Focus outline |

Brand and critical share a hue on purpose: both mean "attention". They never
appear without context. Critical is always text, an icon and a word, never a
colored dot alone.

### Measured contrast (WCAG 2.x)

| Pair | Light | Dark |
| ---- | ----- | ---- |
| foreground on background | 21.0 | 21.0 |
| muted-foreground on background | 5.7 | 8.3 |
| muted-foreground on muted | 5.3 | 7.0 |
| primary-foreground on primary | 21.0 | 21.0 |
| brand-foreground on brand | 5.3 | 5.3 |
| brand-text / destructive on background | 5.3 | 5.5 |
| input border on background (UI, 3:1) | 3.5 | 3.9 |

All text pairs pass AA (4.5:1). The design system page measures these live.

## Typography

IBM Plex Sans for everything, IBM Plex Mono for code, logs, IDs and commit
hashes. Plex was drawn for engineering software, which is what this is. Body
text is 14px.

| Utility | Size / line height | Weight | Tracking | Use |
| ------- | ------------------ | ------ | -------- | --- |
| `type-display` | clamp(28px → 36px) / 1.1 | 600 | -0.02em | Page title |
| `type-metric` | clamp(24px → 32px) / 1.1 | 500 | -0.01em | The big number on a metric card. Tabular figures. |
| `type-title` | 18px / 1.3 | 600 | -0.01em | Section headings |
| `type-body` | 14px / 1.5 | 400 | 0 | Running text, table cells |
| `type-label` | 13px / 1.4 | 500 | 0 | Card titles, table headers, field labels |
| `type-caption` | 12px / 1.4 | 400 | 0 | Units, timestamps, helper text (muted) |
| `type-code` | 13px / 1.5, Plex Mono | 400 | 0 | Code, logs, IDs |

Labels are sentence case. No all-caps eyebrows. Keep line length under 80
characters.

## Shape and elevation

- **Radius:** 0 everywhere. The only round things are the switch thumb and the
  live indicator dot, because a circle is their meaning.
- **Elevation:** flat. Cards are a 1px `border`, not a shadow. Floating layers
  (menus, popovers, dialogs) get one shadow so they read as above the page:
  `0 8px 24px rgb(0 0 0 / 0.12)` in light and a `border` in dark.

## Spacing and layout

- 4px base unit. Inside a control 8–12px, between related items 8px, between
  groups 16px, between sections 32px.
- Content is left aligned, max width 1280px, 16px side gutters on mobile and
  24px from `md`.
- Metric cards sit in a grid that collapses from 4 to 2 to 1 columns. Nothing
  scrolls horizontally at 375px except tables, which scroll inside their own
  container.

## Controls

| Size | Height | Text |
| ---- | ------ | ---- |
| `xs` | 28px | 12px |
| `sm` | 32px | 13px |
| default | 36px | 14px |
| `lg` | 40px | 14px |

Buttons, inputs, selects, toggles and tab lists share these heights so they
align in a row.

### Button variants

| Variant | When |
| ------- | ---- |
| `default` | The main action in a group. One per group. |
| `outline` | Secondary actions next to a primary one. |
| `ghost` | Toolbar and icon actions, actions inside dense rows. |
| `secondary` | A filled alternative when outline is too weak (rare). |
| `brand` | The single most important action on a screen, e.g. *Deploy*. At most one per screen. |
| `destructive` | Actions that delete or stop something. Always confirm with a dialog. |
| `link` | Inline navigation inside text. |

### States

| State | Treatment |
| ----- | --------- |
| Hover | Fills shift one step (`primary` → 85% opacity; ghost/outline get `accent`). |
| Focus | 2px `ring` outline, 2px offset, on every interactive element. Fields also switch their border to `ring`. |
| Active | 1px press (`translate-y-px`). |
| Cursor | `pointer` on everything clickable: buttons, links, tabs, menu and select items, switches. Set globally in `index.css`. |
| Disabled | 50% opacity, `cursor: not-allowed`, no hover. |
| Invalid | `destructive` border plus an error message under the field. |
| Loading | `Spinner` before the label, control disabled, label unchanged. |

## Live data

The overview streams one snapshot per interval over `/api/v1/stream`.

| Connection | Header indicator | Page |
| ---------- | ---------------- | ---- |
| Live | Raspberry dot with a soft pulse, "Live" | Values update in place, without animation. |
| Connecting | Spinner, "Connecting…" | Skeletons until the first snapshot. |
| Reconnecting | "Reconnecting in 3 s" and a *Retry now* button | Last values stay, dimmed to 60%, with "Disconnected. Showing values from 14:09:16." |
| Offline | "Offline" | As reconnecting; retries resume when the browser is back online. |

- Never blank the page on a lost connection: stale data is still useful, as long as it is marked stale.
- The chart range (1 or 5 minutes) is in the URL (`?range=1m`) so a link shows the same view.
- State changes are announced through an `aria-live` region. The countdown is not.

## Data visualization

Charts use Recharts through shadcn's `Chart`, with colors from `--chart-*`.

| Slot | Light | Dark | Stroke |
| ---- | ----- | ---- | ------ |
| `chart-1` | `#cd2355` | `#e5476f` | 2px solid |
| `chart-2` | `#262626` | `#d4d4d4` | 2px dashed (4 3) |

- **At most two series per chart.** The palette has one hue by design. Two series
  are told apart by hue *and* lightness *and* stroke style, and are always
  direct-labelled or come with a legend. More series → small multiples (for
  example one sparkline per CPU core).
- Validated with the dataviz checker: adjacent CVD ΔE 18.3 (light) and 23.1
  (dark), normal-vision ΔE 35.1 / 30.8, both ≥ 3:1 against the surface.
  `chart-2` fails the "lightness band" check, which targets chromatic hues;
  it is a neutral ink series on purpose.
- `chart-3`…`chart-5` are neutral grays for grid lines, reference lines and
  thresholds, never for a data series.
- Single-series charts have no legend; the card title names the series.
- One y-axis per chart. Grid and axes are recessive (`border`, `muted-foreground`).
- Hover shows a crosshair and a tooltip with the exact value and time.
- Values in text use text tokens, never the series color.

## Logo

The mark is a gauge: a stem and a ring in ink, with a raspberry needle and
hub. The wordmark sits to its right. The masters are
`docs/brand/piway-logo-black.svg` and `docs/brand/piway-logo-white.svg`.

| Where | Version |
| ----- | ------- |
| Light backgrounds (light theme, printed docs) | Black |
| Dark backgrounds (dark theme) | White |
| Dashboard header (`components/logo.tsx`) | Inline. Ink is `foreground` and the needle is `brand`, so it follows the theme. |
| Favicon (`public/favicon.svg`) | The mark only, black or white by `prefers-color-scheme` |
| README | Picks the black or white file with `<picture>` |

The needle stays raspberry in both versions. Never recolor the ink, never
put the logo on raspberry, and never stretch it.

## Iconography

Phosphor icons (`@phosphor-icons/react`), regular weight. Icons in buttons
use `data-icon` and take their size from the component. An icon-only button
always has an accessible label and a tooltip.

## Do and don't

| Do | Don't |
| -- | ----- |
| One `default` button per group, others `outline` or `ghost`. | Two filled buttons side by side. |
| Raspberry for the one thing that needs attention. | Raspberry as decoration: borders, headings, icons for their own sake. |
| Status as icon + word + color (`Critical: under-voltage`). | A red dot without words. |
| Tabular numbers with muted units (`42.5 °C`). | Proportional digits that jitter on every update. |
| Hairline borders between regions. | Drop shadows on cards. |
| Semantic tokens (`bg-muted`, `text-muted-foreground`). | Raw colors (`bg-gray-100`, `text-red-600`) or `dark:` overrides in app code. |
| `—` with a tooltip for a value that is not available yet. | `0` for a value that was never measured. |

## Accessibility

- Every interactive element shows the 2px focus outline; the sticky header
  leaves room for it (`scroll-padding-top`).
- Icons are decorative by default (`aria-hidden` via Phosphor's
  `IconContext`). Icon-only buttons carry an `aria-label` and a tooltip.
- Charts have `role="img"` with a summary label, a crosshair tooltip, and a
  table view.
- Units and their numbers never wrap apart (`80\u00a0°C` in code).
- Motion is removed under `prefers-reduced-motion`.
- The theme follows the system until the user picks one; the choice is applied
  before first paint (inline script in `index.html`), so there is no flash.

## Implementation notes

- shadcn/ui on Base UI primitives (`base-lyra` style), Tailwind CSS v4,
  Phosphor icons. Components live in `src/components/ui` and are owned by this
  repo: restyle them through their `cva` variants, not with `className`
  overrides at the call site.
- The theme provider is our own (`src/components/theme-provider.tsx`);
  `next-themes` injected a `<script>` during render, which React 19 reports
  as an error.
- Headings and buttons use sentence case, deliberately, although some
  guidelines (for example Vercel's) prefer Title Case.

## Writing

Plain, specific and sentence case. Name things the way a Pi owner would:
"Temperature", not "Thermal zone 0". Errors say what happened and what to do:
"Can't reach piway. Retrying in 4s." Empty states invite the next action.
