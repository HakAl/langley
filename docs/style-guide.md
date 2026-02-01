# Langley UI Style Guide

Source of truth for the Langley dashboard's visual language. All CSS lives in `web/src/index.css`.

## Visual DNA

- Dense, data-first layout. Minimal ornament.
- Flat UI with sharp corners. No border-radius except two documented exceptions.
- High contrast, strong borders, restrained motion.
- Monospace for data: IDs, methods, status codes, counts, costs, timestamps.
- System font stack for everything else.

This is a monitoring tool. It looks like one. Resist the urge to make it "friendlier."

---

## Design Tokens

All colors come from CSS custom properties on `:root` (dark) and `[data-theme="light"]`. Never hardcode a color value in component CSS.

### Color Palette

| Token | Dark | Light | Usage |
|-------|------|-------|-------|
| `--bg-primary` | `#111318` | `#eeeef4` | Page background |
| `--bg-secondary` | `#161820` | `#ffffff` | Cards, panels, header |
| `--bg-tertiary` | `#1e2028` | `#e2e2ea` | Table headers, hover states, inset areas |
| `--text-primary` | `#f0f0f4` | `#111318` | Body text, headings |
| `--text-secondary` | `#c8c8d0` | `#3a3a44` | Supporting text, labels |
| `--text-muted` | `#9a9aaa` | `#5a5a6a` | Timestamps, hints, placeholders |
| `--accent` | `#e21a41` | `#e21a41` | Primary action, active state, brand mark |
| `--accent-hover` | `#c41535` | `#c41535` | Accent on hover |
| `--accent-dim` | `accent @ 8%` | `accent @ 6%` | Selected item background (via `color-mix`) |
| `--success` | `#22c55e` | `#16a34a` | Connected status, 2xx codes, cost display |
| `--warning` | `#f59e0b` | `#d97706` | 3xx codes, warning severity, PUT method |
| `--error` | `#ef4444` | `#dc2626` | 4xx/5xx codes, error severity, DELETE method, disconnected |
| `--info` | `#06b6d4` | `#0891b2` | Informational anomalies |
| `--sse` | `#8b5cf6` | `#8b5cf6` | SSE badge, streaming indicator |
| `--border` | `#2a2c38` | `#c8c8d4` | Default borders |
| `--border-strong` | `#3a3c4a` | `#a8a8b8` | Table header bottom border |
| `--error-bg` | `error @ 10%` | `error @ 10%` | Error banner background (via `color-mix`) |

### Semantic Color Usage

- **HTTP methods**: GET = `--success`, POST = `--accent`, PUT = `--warning`, DELETE = `--error`
- **Status codes**: 2xx = `.success`, 3xx = `.redirect` (warning), 4xx-5xx = `.error`
- **Anomaly severity**: critical = `--error`, warning = `--warning`, info = `--info`

---

## Typography

| Context | Font | Weight | Size |
|---------|------|--------|------|
| Body / UI | `-apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif` | 400 | `1rem` (16px base) |
| Headings | Same stack | 600-700 | Varies by context |
| Data (IDs, methods, codes, costs, headers, body content) | `monospace` | 500-600 | `0.6875rem` - `0.875rem` |
| Section labels (table headers, detail section titles) | Same stack | 600 | `0.6875rem` - `0.75rem`, uppercase, tracked |
| Badges | Same stack | 700 | `0.5625rem`, uppercase |

**Letter spacing**: Section headers and labels use `0.06em` - `0.1em` tracking. Header title uses `0.08em`.

**Line height**: Global `1.5`.

**Text transform**: Uppercase for section labels, table headers, badges, status labels, and the main title. Body text is sentence case.

---

## Spacing

Base scale in rem (mapped from px at 16px root):

| px | rem | Where used |
|----|-----|-----------|
| 2 | 0.125 | Badge padding (vertical), bar border-radius |
| 4 | 0.25 | Nav gap, icon gaps, small internal padding |
| 6 | 0.375 | Button padding, toggle padding, badge horizontal padding |
| 8 | 0.5 | Flow list gap, filter gap, section gaps, small margins |
| 12 | 0.75 | Flow item padding (vertical), section padding, button horizontal padding |
| 16 | 1.0 | Container padding, card padding, standard section spacing |
| 20 | 1.25 | Button horizontal padding (primary/secondary), modal padding |
| 24 | 1.5 | Settings section padding, stats card padding, heading bottom margin |
| 32 | 2.0 | Empty state vertical padding |

**Defaults**: Use `0.5rem` (8px) for tight spacing, `1rem` (16px) for standard spacing, `1.5rem` (24px) for section separation.

---

## Borders and Corners

| Property | Value |
|----------|-------|
| Default border | `1px solid var(--border)` |
| Strong border | `1px solid var(--border-strong)` (table header bottoms: `2px`) |
| Accent border | `2px solid var(--accent)` (header bottom, detail panel header, help modal header) |
| Left accent | `3px solid var(--border)` on flow items, `3px solid var(--accent)` on detail sections and stat cards |
| Border radius | **`0` everywhere** |

**Two exceptions:**
- `.status-dot`: `border-radius: 50%` (it's a circle)
- `.bar` (chart bars): `border-radius: 2px 2px 0 0` (rounded top corners only)

No shadows anywhere.

---

## Motion

| Property | Duration | Easing |
|----------|----------|--------|
| Nav underline slide | `300ms` | `cubic-bezier(0.5, 0.15, 0.5, 0.85)` |
| Color/border transitions | `300ms` | `cubic-bezier(0.5, 0.15, 0.5, 0.85)` |
| Hover/selection feedback | `150ms` | Default ease (no explicit easing) |
| Chart bar height | `300ms` | Default ease |
| Stat card border | `200ms` | Default ease |

**Easing**: The custom cubic-bezier `(0.5, 0.15, 0.5, 0.85)` is used on all intentional transitions (nav, buttons, inputs, toggles). It produces a gentle ease-in-out.

**Reduced motion**: All transitions and animations are overridden to `0.01ms` via `@media (prefers-reduced-motion: reduce)`. This is enforced globally and cannot be opted out of.

---

## Responsive Breakpoints

| Breakpoint | What changes |
|------------|-------------|
| `<= 1200px` | Main content stacks vertically. Flow detail panel goes full-width below the flow list. |
| `<= 768px` | Header wraps. Nav moves to full-width row. Flow items compress to 3-column grid. Tokens and timestamps span full row. |
| `<= 480px` | Nav becomes horizontally scrollable (hidden scrollbar). Nav buttons shrink. |
| `pointer: coarse` | All interactive elements enforce `44px` minimum touch target (nav buttons, toggles, close buttons, primary/secondary buttons, clear buttons). |

---

## Component Rules

### Buttons

**Primary** (`.primary-btn`):
- Background: `--accent`, text: white
- Hover: `--accent-hover`
- Active: `opacity: 0.8`
- Disabled: `opacity: 0.5`, `cursor: not-allowed`
- Focus: `2px solid var(--accent)`, `outline-offset: 2px`

**Secondary** (`.secondary-btn`):
- Background: transparent, border: `1px solid var(--border)`, text: `--text-secondary`
- Hover: border becomes `--accent`, text becomes `--text-primary`
- Active: `opacity: 0.8`
- Focus: same as primary

**Icon/utility buttons** (`.close-btn`, `.filter-clear-btn`, `.link-btn`):
- No background, no border
- Minimum size `32px` (desktop), `44px` (touch)
- Focus: `2px solid var(--accent)`, `outline-offset: 2px`

**All buttons must have all four states**: hover, active, focus-visible, disabled (where applicable).

### Inputs and Selects

- Background: `--bg-primary`
- Border: `1px solid var(--border)`
- Focus: border-color changes to `--accent`, plus `2px` outline ring
- Font size: `0.875rem`
- Every input needs a visible `<label>` or `aria-label`

### Tables (`.data-table`)

- Header: `--bg-tertiary` background, `2px solid var(--border-strong)` bottom border
- Header text: `0.6875rem`, uppercase, `letter-spacing: 0.1em`, `--text-muted`
- Row hover: `--bg-tertiary` background
- Keyboard-selected row: `2px solid var(--accent)` outline, `outline-offset: -2px`
- Clickable rows: `cursor: pointer`

### Cards and Panels

- Background: `--bg-secondary`
- Border: `1px solid var(--border)`
- No shadows, no rounding
- Section headers inside panels: `--bg-tertiary` background, `--accent` bottom border

### Flow Items (`.flow-item`)

- 5-column grid: method, host/path, status code, tokens, timestamp
- Default left border: `3px solid var(--border)`
- Hover: background to `--bg-tertiary`, left border to `--text-muted`
- Selected: all borders to `--accent`, background to `--accent-dim`
- At 768px: collapses to 3-column grid

### Badges (`.badge`)

- `0.5625rem`, uppercase, `font-weight: 700`, `letter-spacing: 0.06em`
- Default: `--accent` background, white text
- Variants: `.sse` (`--sse`), `.error` (`--error`), `.warning` (`--warning`)
- No rounding

### Modals

- Backdrop: `rgba(0, 0, 0, 0.7)`, fixed position, full viewport
- Panel: `--bg-secondary`, `1px solid var(--border)`, max-width constrained
- Header: `--accent` bottom border
- Must trap focus. `Esc` closes.

### Stat Cards (`.stat-card`)

- `--bg-secondary` background, `1px solid var(--border)`
- Top border: `3px solid var(--accent)` (hover: `--accent-hover`)
- Value: `2rem`, `font-weight: 700`, monospace, `--accent` color
- Label: `0.75rem`, uppercase, `--text-muted`

### Empty States (`.empty-state`)

- Centered text, `--text-muted`
- Dashed border: `1px dashed var(--border)`
- `--bg-secondary` background
- Heading: uppercase, `--text-secondary`

---

## Accessibility

### Contrast

- Normal text on background: >= 4.5:1 ratio
- Large text (>= 18px or >= 14px bold): >= 3:1 ratio
- Non-text UI (borders, icons, focus rings): >= 3:1 ratio

### Focus

Every interactive element must have a visible `:focus-visible` style. Standard pattern:

```css
.element:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
```

Never remove focus styles. Never use `outline: none` without providing an alternative.

### Touch Targets

- Desktop minimum: `32px` height and width
- Touch (`pointer: coarse`): `44px` minimum, enforced via media query
- This applies to: nav buttons, toggles, close/clear buttons, primary/secondary buttons, token input button

### Keyboard Navigation

- All interactive elements reachable via `Tab`
- Lists support `j`/`k` navigation and `Enter` to select
- Modals trap focus
- `Esc` closes modals and detail panels
- Keyboard-selected items show `2px solid var(--accent)` outline

### Error States

- Error banners use `role="alert"`
- Error text uses `--error` color, distinct from surrounding content
- Never rely on color alone to convey meaning; pair with text or icon

### Reduced Motion

Globally enforced. No opt-out:

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    transition-duration: 0.01ms !important;
    animation-duration: 0.01ms !important;
  }
}
```

---

## Do / Don't

| Do | Don't |
|----|-------|
| Use `var(--token-name)` for all colors | Hardcode hex values in component CSS |
| Add all four interaction states to new buttons | Ship a button with only a hover state |
| Use monospace for data values | Use monospace for labels or prose |
| Keep border-radius at 0 | Add rounding "just for this component" |
| Use `letter-spacing` and `text-transform: uppercase` for section headers | Use font-size alone to create hierarchy |
| Test at all three breakpoints (1200, 768, 480) | Assume desktop-only |
| Provide `aria-label` when there's no visible text label | Rely on placeholder text as a label |
| Respect the spacing scale | Invent new magic-number spacing values |
