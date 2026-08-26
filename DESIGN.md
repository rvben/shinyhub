---
name: ShinyHub
description: A calm, performance-led control surface for delivering fast and reliable dashboards.
colors:
  deep-space: "#030510"
  constellation-canvas: "#060914"
  control-surface: "#0E1426"
  control-surface-raised: "#141B32"
  control-surface-hover: "#1B2444"
  quiet-line: "#1E2A4A"
  strong-line: "#2B3A63"
  starlight-text: "#E8EEFF"
  soft-starlight: "#A8B4D4"
  muted-starlight: "#6B7AA3"
  signal-cyan: "#38BDF8"
  soft-cyan: "#7DD3FC"
  sparkle-blue: "#BAE6FD"
  electric-blue: "#60A5FA"
  running-green: "#4ADE80"
  warning-amber: "#FBBF24"
  failure-coral: "#F87171"
  standby-indigo: "#A5B4FC"
  daylight-canvas: "#F4F7FC"
  daylight-inset: "#E8EDF6"
  daylight-surface: "#FFFFFF"
  daylight-surface-raised: "#F3F6FB"
  daylight-surface-hover: "#E9EEF7"
  daylight-line: "#DBE2EE"
  daylight-line-strong: "#C6D0E0"
  daylight-text: "#16203A"
  daylight-text-soft: "#45526E"
  daylight-text-muted: "#5B6784"
  daylight-signal: "#0369A1"
  daylight-running: "#15803D"
  daylight-warning: "#B45309"
  daylight-failure: "#DC2626"
  daylight-standby: "#4F46E5"
typography:
  display:
    fontFamily: "Manrope, -apple-system, system-ui, Segoe UI, sans-serif"
    fontSize: "2.6rem"
    fontWeight: 200
    lineHeight: 1
    letterSpacing: "-0.04em"
  headline:
    fontFamily: "Manrope, -apple-system, system-ui, Segoe UI, sans-serif"
    fontSize: "1.7rem"
    fontWeight: 700
    lineHeight: 1.2
    letterSpacing: "-0.02em"
  title:
    fontFamily: "Manrope, -apple-system, system-ui, Segoe UI, sans-serif"
    fontSize: "1.05rem"
    fontWeight: 600
    lineHeight: 1.3
    letterSpacing: "-0.01em"
  body:
    fontFamily: "Manrope, -apple-system, system-ui, Segoe UI, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.55
    letterSpacing: "-0.005em"
  label:
    fontFamily: "Manrope, -apple-system, system-ui, Segoe UI, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 600
    lineHeight: 1.35
    letterSpacing: "0.02em"
  mono:
    fontFamily: "Space Mono, JetBrains Mono, Cascadia Code, Consolas, monospace"
    fontSize: "0.82rem"
    fontWeight: 400
    lineHeight: 1.5
    letterSpacing: "-0.01em"
rounded:
  focus: "3px"
  sm: "4px"
  md: "8px"
  lg: "14px"
  pill: "99px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
  xl: "32px"
components:
  button-primary:
    backgroundColor: "{colors.signal-cyan}"
    textColor: "{colors.deep-space}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "8px 18px"
  button-secondary:
    backgroundColor: "{colors.control-surface}"
    textColor: "{colors.soft-starlight}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  button-danger:
    backgroundColor: "{colors.failure-coral}"
    textColor: "{colors.deep-space}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "9px 18px"
  card:
    backgroundColor: "{colors.control-surface-raised}"
    textColor: "{colors.starlight-text}"
    rounded: "{rounded.lg}"
    padding: "14px 16px"
  input:
    backgroundColor: "{colors.control-surface}"
    textColor: "{colors.starlight-text}"
    typography: "{typography.mono}"
    rounded: "{rounded.md}"
    padding: "9px 12px"
  status:
    backgroundColor: "{colors.control-surface}"
    textColor: "{colors.soft-starlight}"
    typography: "{typography.label}"
    rounded: "{rounded.pill}"
    padding: "4px 10px"
---

# Design System: ShinyHub

## 1. Overview

**Creative North Star: "The Calm Control Room"**

ShinyHub is a precise, quietly energetic control surface for delivering performant dashboards. Its Constellation identity uses deep tonal layers, starlight text, and sparing cyan signals to make operational state legible without turning the interface into a wall of instrumentation. The Orbit Hub lockup—a crystalline signal held by three connected orbital nodes—is the product signature; it appears as a full wordmark where recognition matters and as the standalone mark only where space is genuinely compact.

The system is dark-first but not dark-only. The light theme carries the same semantic hierarchy with contrast-adjusted colors, while white-label branding can replace front-door identity without weakening the operator console. Density is purposeful: familiar controls, compact information, and clear grouping keep experts moving without making occasional users decode an unfamiliar system.

The interface must never resemble a complex hyperscale cloud console, a fragile developer demo tool, or a visually noisy and flashy SaaS dashboard. Every flourish must either identify ShinyHub, communicate state, or improve orientation.

**Key Characteristics:**

- Deep tonal surfaces with crisp, low-contrast boundaries.
- Cyan reserved for identity, focus, selection, and decisive action.
- Semantic status colors paired with text or shape, never used as the only signal.
- Compact product typography with monospaced technical values.
- Responsive structure, visible keyboard focus, and motion that yields to reduced-motion preferences.

**The Performance-to-Pixels Rule.** Visual hierarchy must lead from dashboard health and availability to the action that improves them.

## 2. Colors

The Constellation palette is a restrained deep-space neutral system animated by rare, high-clarity signals; the daylight theme preserves the same roles with WCAG-conscious darker accents.

### Primary

- **Signal Cyan** (#38BDF8): the dark-theme brand signal for primary actions, active navigation icons, focus, links, and the Orbit Hub identity.
- **Daylight Signal** (#0369A1): the contrast-safe light-theme equivalent of Signal Cyan.

### Secondary

- **Soft Cyan** (#7DD3FC): supporting information and technical readouts on dark surfaces.
- **Electric Blue** (#60A5FA): the second stop in primary-action gradients; it strengthens action hierarchy without introducing another hue family.
- **Sparkle Blue** (#BAE6FD): the brightest identity glint, reserved for the Orbit Hub core and subtle highlights.

### Tertiary

- **Running Green** (#4ADE80): healthy and running states.
- **Warning Amber** (#FBBF24): deploying, waking, constrained, or attention states.
- **Failure Coral** (#F87171): failed, crashed, destructive, and error states.
- **Standby Indigo** (#A5B4FC): hibernated or ready-to-wake states; distinct from both failure and active service.

### Neutral

- **Deep Space** (#030510) and **Constellation Canvas** (#060914): the dark inset and page foundations.
- **Control Surface**, **Raised Control Surface**, and **Hover Control Surface** (#0E1426, #141B32, #1B2444): the dark tonal elevation ladder.
- **Quiet Line** and **Strong Line** (#1E2A4A, #2B3A63): default and emphasized boundaries.
- **Starlight Text**, **Soft Starlight**, and **Muted Starlight** (#E8EEFF, #A8B4D4, #6B7AA3): primary, supporting, and tertiary dark-theme text.
- **Daylight Canvas**, **Daylight Surface**, and **Daylight Raised Surface** (#F4F7FC, #FFFFFF, #F3F6FB): the light-theme foundation and surface ladder.
- **Daylight Text**, **Daylight Soft Text**, and **Daylight Muted Text** (#16203A, #45526E, #5B6784): primary, supporting, and tertiary light-theme text.

**The Signal Rarity Rule.** Cyan is functional, not decorative. It marks identity, focus, selection, or the highest-priority action and must not wash across inactive surfaces.

**The Semantic Pairing Rule.** Green, amber, coral, and indigo must be paired with a label, icon, position, or state shape. Color alone is never sufficient.

## 3. Typography

- **Display Font:** Manrope (with system sans-serif fallbacks)
- **Body Font:** Manrope (with system sans-serif fallbacks)
- **Label/Mono Font:** Space Mono (with JetBrains Mono, Cascadia Code, and Consolas fallbacks)

**Character:** Manrope keeps the operator surface human, compact, and direct; Space Mono distinguishes slugs, commands, metrics, tokens, and machine-facing values. The pairing feels technical without becoming terminal-themed.

### Hierarchy

- **Display** (200, 2.6rem, 1): route-level toolbar headings only; its light weight and tight tracking create one clear visual landmark.
- **Headline** (700, 1.7rem, 1.2): primary app or overview headings where a route needs more compact authority.
- **Title** (600, 1.05rem, 1.3): settings blocks, modal groups, and substantive card sections.
- **Body** (400, 0.875rem, 1.55): instructions and explanatory copy; keep prose near 64ch whenever the layout allows.
- **Label** (600, 0.75rem, 0.02em): compact metadata, badges, and control labels. Uppercase is reserved for exceptional micro-labels, not ordinary navigation.
- **Mono** (400, 0.82rem, 1.5): slugs, command snippets, token values, logs, and tabular technical data.

**The One Sans Rule.** Manrope carries all product hierarchy. Never introduce a display face merely to make an operational screen feel branded.

**The Mono Means Machine Rule.** Space Mono signals data with machine semantics. It is forbidden for ordinary paragraphs, navigation, or primary actions.

## 4. Elevation

ShinyHub uses a hybrid system: tonal layering and hairline borders establish depth at rest; diffuse ambient shadows are reserved for authentication, modals, floating menus, and decisive primary actions. Cards should normally become one tonal step brighter on hover instead of lifting dramatically.

### Shadow Vocabulary

- **Lifted Card** ("0 24px 60px -20px rgba(0,0,0,0.6)"): dark-theme authentication and other singular elevated cards.
- **Daylight Lifted Card** ("0 18px 40px -24px rgba(22,32,58,0.28)"): contrast-adjusted light-theme equivalent.
- **Modal Focus** ("0 32px 80px rgba(0,0,0,0.7)"): focused dialogs above a blurred overlay.
- **Primary Action** ("0 4px 14px rgba(56,189,248,0.25)"): low cyan lift for decisive actions, strengthened only on hover.
- **Launchpad Hover** ("0 10px 30px -16px rgba(0,0,0,0.8)"): restrained feedback for a launchable dashboard tile.

**The Tonal-First Rule.** A resting surface earns hierarchy with background and border tokens before it earns a shadow.

**The One Floating Plane Rule.** Only the active overlay, popover, or decisive control may read as floating. If every card casts a shadow, the hierarchy has failed.

## 5. Components

Components are compact, familiar, and confident. Every interactive primitive must cover default, hover, focus-visible, active where meaningful, disabled, loading, and error states.

### Buttons

- **Shape:** gently curved rectangle (8px), with a compact 40px interaction height where buttons sit in action rows.
- **Primary:** Signal Cyan to Electric Blue gradient, Deep Space text, 700 weight, and compact 8px × 18px padding.
- **Hover / Focus:** brighten and translate upward by at most 1px; use a visible cyan focus ring. Reduced motion removes translation without removing feedback.
- **Secondary / Ghost:** translucent Control Surface background, Strong Line border, Soft Starlight text; hover changes border and text to Signal Cyan.
- **Danger:** Failure Coral fill with Deep Space text; never style a destructive action like the primary cyan action.

### Chips

- **Style:** 99px pill geometry for metadata badges; semantic status badges include a 5px dot and a readable text label.
- **State:** running may breathe gently; working, attention, and standby states hold steady; stopped and unknown states remain neutral.

### Cards / Containers

- **Corner Style:** softly rounded (14px).
- **Background:** Raised Control Surface at rest, Hover Control Surface on hover; the light theme maps to its daylight equivalents.
- **Shadow Strategy:** flat by default, following the Tonal-First Rule.
- **Border:** one-pixel Quiet Line, strengthening to Strong Line on hover or emphasis.
- **Internal Padding:** typically 14px × 16px for dense application cards and 16px × 18px for viewer launch tiles.

### Inputs / Fields

- **Style:** Control Surface background, one-pixel Quiet Line border, 8px corners, and 9px × 12px padding. Use Space Mono when the value is technical; use Manrope for natural-language search and authentication.
- **Focus:** Signal Cyan border plus a translucent 3px cyan halo; never remove focus without replacing it.
- **Error / Disabled:** Failure Coral and a readable error surface for errors; disabled controls retain their shape and label at reduced emphasis.

### Navigation

The desktop shell uses a 248px translucent sidebar that can collapse to a 68px icon rail. Navigation items use 8px corners, muted text at rest, a tonal hover, and an inset one-pixel boundary for the active item. At 860px and below, the sidebar becomes an off-canvas drawer behind a sticky mobile top bar. Active state, keyboard focus, and availability remain distinct.

Top-level app pages use separate injected navigation chrome that identifies itself and the current app without reserving app layout. Excluded inside frames and isolated from the app in a closed shadow root, it defaults to a 264px × 40px top-centre bar with the current app name and a switch action. The control stays in the Constellation world through a dark surface, compact Manrope type, cyan focus, and restrained elevation; it opens a bounded 320px app-list popover and retains lazy loading, deliberate focus handling, Escape-to-close behavior, and explicit unavailable, error/retry, empty, and filtered-empty states.

Visitors can drag the control with a pointer or touch, or use its keyboard-operable position menu, to snap it to top-centre, top-right, left-centre, or right-centre. Side placements become 40px × 104px vertical controls, and the chosen position is stored per app in localStorage. Closing the control reduces it to a 30px restore tab and scopes that dismissal to the current browser tab in sessionStorage. At 520px and below, the initial 240px teaching bar contracts after eight seconds idle to a 140px **Apps** pill and expands again on use.

### Application Status

Application state is information, not decoration. Use a dot-and-label badge or a labeled metric; reserve animated glow for live healthy state, disable it under reduced motion, and keep viewer-facing availability less revealing than operator-facing health.

### Dashboard Launch Tile

Viewer tiles use a 14px corner, a 46px app avatar, a strong app name, restrained description, and a directional affordance. The tile may lift by 2px on hover, but the dashboard itself—not decorative chrome—remains the destination.

**The Familiar Controls Rule.** Buttons look like buttons, tabs like tabs, and forms like forms. Never invent an affordance to make a standard operation feel novel.

## 6. Do's and Don'ts

### Do:

- **Do** prioritize dashboard performance, availability, and the next useful action in every hierarchy.
- **Do** use Signal Cyan (#38BDF8) sparingly for identity, active state, focus, and decisive action.
- **Do** use 8px control corners and 14px container corners to keep geometry consistent.
- **Do** pair every semantic color with a readable label or non-color cue.
- **Do** preserve complete dark and light themes, visible keyboard focus, WCAG 2.2 AA contrast, and reduced-motion behavior.
- **Do** use skeletons for loading and actionable empty states that teach the next step.
- **Do** keep responsive changes structural: collapse navigation, reflow grids, and protect touch targets.

### Don't:

- **Don't** make ShinyHub feel like a complex hyperscale cloud console; expose advanced capability progressively instead of presenting every control at once.
- **Don't** make ShinyHub feel like a fragile developer demo tool; show durable states, consequences, progress, and recovery.
- **Don't** make ShinyHub feel like a visually noisy and flashy SaaS dashboard; prohibit decorative gradients, gratuitous glow, and competing accents.
- **Don't** use cyan, green, amber, coral, or indigo as decoration or as the only carrier of meaning.
- **Don't** add heavy shadows to resting cards or stack multiple floating planes.
- **Don't** introduce display fonts, terminal-themed body copy, custom scrollbars, or nonstandard form controls for flavor.
- **Don't** animate page-load choreography; motion communicates state and completes within roughly 200ms, except for subtle ambient health cues.
