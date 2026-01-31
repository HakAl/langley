# Langley UI Style Guide

This guide codifies the existing Langley UI look and adds baseline UX and accessibility standards.
The intent is to preserve the current visual character while enforcing consistency.

## Visual DNA (Keep As-Is)
- Dense, data-first layout; minimal ornament.
- Flat UI with sharp corners (no rounding).
- High contrast, strong borders, restrained motion.
- Monospace for codes, IDs, counts, and method badges.

## Design Tokens (Current)
Use these tokens as the source of truth. Do not hardcode colors.

### Theme tokens
- --bg-primary
- --bg-secondary
- --bg-tertiary
- --text-primary
- --text-secondary
- --text-muted
- --accent
- --accent-hover
- --success
- --warning
- --error
- --info
- --border
- --sse
- --error-bg

### Typography
- UI font: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif
- Data font: monospace (IDs, methods, counts, costs)
- Weight: 400-700

### Spacing
- Base scale (px): 2, 4, 6, 8, 12, 16, 20, 24, 32 (rem units in CSS)
- Prefer 8px and 16px for layout spacing

### Borders and Corners
- 1px solid border using --border
- Border radius: 0 (exceptions: .status-dot circle, .bar 2px top corners)

### Motion
- Transitions only for state feedback (hover, focus)
- Durations: 150ms to 300ms
- No decorative animation (exception: chart bar height transition)

## Global Standards (Baseline)
- Contrast: normal text >= 4.5:1, large text >= 3:1
- Non-text UI (icons, borders, focus rings) >= 3:1
- Interactive size: minimum 32px for desktop; 44px for touch (pointer: coarse)
- Keyboard: all interactive elements reachable via Tab
- Focus: 2px accent outline ring via :focus-visible
- Motion: respect prefers-reduced-motion

## Component Rules

### Buttons
- Primary: --accent background, white text
- Secondary: transparent, 1px border
- States: hover, active, disabled, focus-visible
- Disabled: reduce opacity, keep text readable

### Inputs and Selects
- Background: --bg-primary
- Border: 1px solid --border
- Focus: 2px accent outline ring (inputs also change border-color)
- Provide labels or aria-label

### Tables
- Header row: --bg-tertiary, uppercase, small type
- Row hover: --bg-tertiary
- Keyboard-selected row: 2px accent outline (click-selected: accent border)

### Cards and Panels
- Background: --bg-secondary
- Border: 1px solid --border
- No shadows

### Badges
- Uppercase, small type
- Colors: --accent (default), --sse (SSE), --error, --warning. Missing: --success, --info variants.
- No rounding

### Modals
- Backdrop: 70% black overlay
- Centered panel with 1px border
- Focus trap, ESC closes

## Accessibility Rules
- All form inputs have a visible label or aria-label
- Error text uses role="alert" and is color-distinct
- Provide text alternatives for icons and status
- Ensure keyboard navigation for lists and modals

## Usage Guidance
- Only use token variables in CSS
- Keep visual hierarchy via spacing and text color, not shadows
- Preserve current density; do not enlarge without need
- New components must include all interaction states
