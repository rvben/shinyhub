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
  safety-amber-action: "#F59E0B"
  safety-amber-rail: "#21170A"
  safety-amber-line: "#A86608"
  safety-amber-ink: "#241604"
  safety-amber-copy: "#FFF8E7"
  safety-amber-copy-soft: "#F7DCA3"
  safety-amber-deadline: "#FFE9B8"
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
  button-support:
    backgroundColor: "{colors.safety-amber-action}"
    textColor: "{colors.safety-amber-ink}"
    typography: "{typography.label}"
    rounded: "{rounded.md}"
    padding: "7px 11px"
  support-rail:
    backgroundColor: "{colors.safety-amber-rail}"
    textColor: "{colors.safety-amber-copy}"
    typography: "{typography.label}"
    padding: "8px clamp(12px, 2vw, 24px)"
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

## Overview

**Creative North Star: "The Calm Control Room"**

ShinyHub is a precise, quietly energetic control surface for delivering performant dashboards. Its Constellation identity uses deep tonal layers, starlight text, and sparing cyan signals to make operational state legible without turning the interface into a wall of instrumentation. The Orbit Hub lockup—a crystalline signal held by three connected orbital nodes—is the product signature; it appears as a full wordmark where recognition matters and as the standalone mark only where space is genuinely compact.

The system is dark-first but not dark-only. The light theme carries the same semantic hierarchy with contrast-adjusted colors, while white-label branding can replace front-door identity without weakening the operator console. Density is purposeful: familiar controls, compact information, and clear grouping keep experts moving without making occasional users decode an unfamiliar system.

Temporary security states remain native to Constellation inside the admin workflow. When that state crosses into arbitrary app content, one persistent amber safety rail carries the platform boundary with it: the rail names who is acting, who is represented, whether actions can mutate data, when authority ends, and how to exit.

The interface must never resemble a complex hyperscale cloud console, a fragile developer demo tool, or a visually noisy and flashy SaaS dashboard. Every flourish must either identify ShinyHub, communicate state, or improve orientation.

**Key Characteristics:**

- Deep tonal surfaces with crisp, low-contrast boundaries.
- Cyan reserved for identity, focus, selection, and decisive action.
- Semantic status colors paired with text or shape, never used as the only signal.
- One unmistakable amber rail for temporary cross-world security states.
- Compact product typography with monospaced technical values.
- Responsive structure, visible keyboard focus, and motion that yields to reduced-motion preferences.

**The Performance-to-Pixels Rule.** Visual hierarchy must lead from dashboard health and availability to the action that improves them.

## Colors

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
- **Safety Amber** (#F59E0B) on **Safety Rail** (#21170A): the action and foundation for a temporary security state that must remain legible over arbitrary app content. Safety Line, Safety Copy, Soft Safety Copy, and Safety Deadline provide the rail's border and text hierarchy without borrowing the app's palette.
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

**The Amber Safety-State Rule.** Use the full amber treatment only for an active security or safety boundary with an explicit consequence and exit. Keep the host application visually untouched so the rail remains the single unmistakable platform signal.

## Typography

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

## Layout

The admin workflow remains inside the established responsive Constellation shell. Cross-world safety chrome is independent of both that shell and the hosted app: mount it as a full-width, sticky direct child of the document so it reserves its own height, stays visible while the app scrolls, and does not depend on the app's layout or stacking context.

At 620px and below, wrap the safety rail rather than truncate its meaning. Keep the warning mark beside the actor-and-subject copy, then place the tabular deadline beneath the copy and the exit action at the trailing edge. Actor, subject, mutability, deadline, and exit must all remain visible without horizontal scrolling.

**The Cross-World Boundary Rule.** Platform safety chrome owns its own layout and must remain legible across arbitrary app themes, frameworks, and responsive behavior.

## Elevation & Depth

ShinyHub uses a hybrid system: tonal layering and hairline borders establish depth at rest; diffuse ambient shadows are reserved for authentication, modals, floating menus, and decisive primary actions. Cards should normally become one tonal step brighter on hover instead of lifting dramatically.

### Shadow Vocabulary

- **Lifted Card** ("0 24px 60px -20px rgba(0,0,0,0.6)"): dark-theme authentication and other singular elevated cards.
- **Daylight Lifted Card** ("0 18px 40px -24px rgba(22,32,58,0.28)"): contrast-adjusted light-theme equivalent.
- **Modal Focus** ("0 32px 80px rgba(0,0,0,0.7)"): focused dialogs above a blurred overlay.
- **Primary Action** ("0 4px 14px rgba(56,189,248,0.25)"): low cyan lift for decisive actions, strengthened only on hover.
- **Launchpad Hover** ("0 10px 30px -16px rgba(0,0,0,0.8)"): restrained feedback for a launchable dashboard tile.
- **Safety Rail Separation** ("0 8px 24px rgba(15,9,2,0.28)"): a restrained downward shadow that separates the persistent amber rail from arbitrary app content without making it feel like a detachable overlay.

**The Tonal-First Rule.** A resting surface earns hierarchy with background and border tokens before it earns a shadow.

**The One Floating Plane Rule.** Only the active overlay, popover, or decisive control may read as floating. If every card casts a shadow, the hierarchy has failed.

## Components

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

### Operational Data Surfaces

- **Metric summary:** lead with three or four decision-driving measures in a semantic description list. Keep the strip flat, using block borders and hairline dividers instead of separate cards; set values in tabular monospaced type and keep labels and explanatory notes quiet. Reflow four columns to two before values become cramped.
- **Analysis split:** pair the dominant trend or activity view with a narrower context or composition rail inside one 14px tonal container. Collapse to one column and move the divider between rows when either side can no longer read comfortably.
- **Charts:** use restrained grid lines, monospaced axes, and Signal Cyan only for the principal series. An SVG chart needs a concise accessible name and a complete semantic data table immediately adjacent but visually hidden; the visual is never the sole source of exact values or meaning.
- **Operational tables:** keep real table semantics, compact rows, muted headers, and tabular monospaced numeric columns. Lead with the identifying field; at narrow widths allocate columns deliberately and truncate only secondary text, preserving horizontal scrolling when exact values would otherwise be lost.
- **Motion:** a chart may reveal once to establish reading direction, without stagger or replay. Keep the reveal restrained, and remove it entirely under `prefers-reduced-motion` without hiding data or feedback.
- **Async replacement:** mark loading regions busy, announce loading and successful completion politely, announce failures assertively with an actionable retry, and restore focus to the equivalent initiating control when user-triggered refreshes replace the DOM. Background refreshes must not steal focus.

**The Data Has a Text Twin Rule.** Every visual summary of operational data must retain a semantic text equivalent that exposes the same labels and values.

**The Refresh Does Not Disorient Rule.** Replacing an async region must preserve the operator's place through stable focus restoration and explicit loading, success, and error announcements.

### Inputs / Fields

- **Style:** Control Surface background, one-pixel Quiet Line border, 8px corners, and 9px × 12px padding. Use Space Mono when the value is technical; use Manrope for natural-language search and authentication.
- **Focus:** Signal Cyan border plus a translucent 3px cyan halo; never remove focus without replacing it.
- **Error / Disabled:** Failure Coral and a readable error surface for errors; disabled controls retain their shape and label at reduced emphasis.

### Navigation

The desktop shell uses a 232px translucent sidebar that can collapse to a 48px icon rail. Its expanded brand lockup remains ShinyHub-first, with an operator-provided instance title rendered as a restrained subtitle; the collapsed rail uses the standalone ShinyHub mark. Navigation items use 8px corners, muted text at rest, a tonal hover, and an inset one-pixel boundary for the active item. The footer action says **About ShinyHub**, making product identity explicit even when external authentication bypasses the built-in login. At 860px and below, the sidebar becomes an off-canvas drawer behind a sticky mobile top bar and retains the same lockup/subtitle hierarchy. Active state, keyboard focus, and availability remain distinct.

Top-level app pages use separate injected navigation chrome that identifies itself and the current app without reserving app layout. Excluded inside frames and isolated from the app in a closed shadow root, it defaults to a 264px × 40px top-right bar with the current app slug and a switch action. The label stays stable while the app list loads; friendly names appear in the list without renaming the trigger mid-interaction. The app list preserves the dashboard's familiar alphabetical grouping as its baseline, then promotes the current group and its “Here” row so nearby destinations and present context are immediately legible. Choosing another app immediately keeps the panel open, names the destination as opening, marks its row with a reduced-motion-safe progress state, and blocks duplicate same-tab switches until navigation completes; modified clicks retain native new-tab behavior. The control stays in the Constellation world through a dark surface, compact Manrope type, cyan focus, and restrained elevation; it opens a bounded 320px app-list popover and retains lazy loading, deliberate focus handling, Escape-to-close behavior, and explicit unavailable, error/retry, empty, and filtered-empty states.

Visitors can drag the control with a pointer or touch, or use its keyboard-operable position menu, to snap it to top-centre, top-right, left-centre, or right-centre. Side placements become 40px × 104px vertical controls, and the chosen position is stored once per hub in localStorage so it follows the visitor across apps. Closing the control reduces it to a 30px restore tab and scopes that dismissal to the current browser tab in sessionStorage. At 520px and below, the initial 240px teaching bar contracts after eight seconds idle to a 140px **Apps** pill and expands again on use.

### Support-Session Confirmation

The confirmation dialog is an app-scoped safety checkpoint inside the admin shell, not a generic impersonation prompt. Keep the ordinary modal geometry and typography, then replace cyan emphasis with amber only where it communicates risk: the warning mark, mutability note, focus treatment, and affirmative action.

- **Actor / subject language:** say that the administrator will enter one app as the named subject. Never collapse the real actor and represented subject into one ambiguous identity.
- **Scope and mutability:** state the app scope, hard duration, audit reason, and the sentence **This is not read-only.** Explain both what can change and which privileged surfaces remain inaccessible.
- **Pending action:** after submission begins, set the dialog busy, disable the submit, cancel, and close controls, and ignore overlay and Escape dismissal. Restore all dismissal paths only when a failed request is ready for correction; successful creation navigates directly into the supported app.

**The Pending Means Committed Rule.** Once a security-sensitive action is in flight, the modal cannot imply cancellation by disappearing while the request may still succeed.

### Persistent Support-Session Rail

The support-session rail is platform-owned safety chrome placed above arbitrary app content. Isolate its CSS from the app, mount it before the app body as a sticky direct document child, and repair that placement if a hosted SPA replaces document children. Maintain a document-level active marker so other platform chrome can detect the state without reaching into the rail.

- **Message order:** lead with **Support session · Viewing as [subject]**; follow with **[actor] is the administrator** and an explicit statement that actions can change the subject's data. The visible deadline and **End support session** action complete the line of accountability.
- **Countdown:** update the visible tabular timer each second, but keep assistive announcements quiet. Announce only meaningful thresholds—five minutes, one minute, and expiry—through a polite atomic live region; never speak every tick.
- **Exit:** while ending, disable the action and label it **Ending…**. If ending fails, restore an explicit retry action and explain that automatic expiry remains in force.
- **Responsive behavior:** at 620px and below, wrap into a compact multi-row rail while preserving every safety fact and an easy-to-reach exit action.
- **Self-healing:** hosted app DOM churn must not remove, bury, or restyle the rail. Reinsert it at the document boundary when needed, without duplicating it.

**The No Invisible Identity Switch Rule.** A represented session is never communicated by account data alone; the persistent rail must continuously expose actor, subject, mutability, deadline, and exit.

### Application Status

Application state is information, not decoration. Use a dot-and-label badge or a labeled metric; reserve animated glow for live healthy state, disable it under reduced motion, and keep viewer-facing availability less revealing than operator-facing health.

### Dashboard Launch Tile

Viewer tiles use a 14px corner, a 46px app avatar, a strong app name, restrained description, and a directional affordance. The tile may lift by 2px on hover, but the dashboard itself—not decorative chrome—remains the destination.

**The Familiar Controls Rule.** Buttons look like buttons, tabs like tabs, and forms like forms. Never invent an affordance to make a standard operation feel novel.

## Do's and Don'ts

### Do:

- **Do** prioritize dashboard performance, availability, and the next useful action in every hierarchy.
- **Do** use Signal Cyan (#38BDF8) sparingly for identity, active state, focus, and decisive action.
- **Do** use 8px control corners and 14px container corners to keep geometry consistent.
- **Do** pair every semantic color with a readable label or non-color cue.
- **Do** preserve complete dark and light themes, visible keyboard focus, WCAG 2.2 AA contrast, and reduced-motion behavior.
- **Do** use skeletons for loading and actionable empty states that teach the next step.
- **Do** keep responsive changes structural: collapse navigation, reflow grids, and protect touch targets.
- **Do** pair operational charts with semantic text equivalents and preserve focus across user-triggered refreshes.
- **Do** use actor, subject, and mutability as separate concepts whenever authority is temporarily represented.
- **Do** keep security-sensitive confirmation dialogs open and visibly busy while their request is pending.
- **Do** keep countdowns visually precise but announce only meaningful time thresholds to assistive technology.
- **Do** mount cross-world safety chrome at the document boundary and make it self-heal after hosted-app DOM replacement.

### Don't:

- **Don't** make ShinyHub feel like a complex hyperscale cloud console; expose advanced capability progressively instead of presenting every control at once.
- **Don't** make ShinyHub feel like a fragile developer demo tool; show durable states, consequences, progress, and recovery.
- **Don't** make ShinyHub feel like a visually noisy and flashy SaaS dashboard; prohibit decorative gradients, gratuitous glow, and competing accents.
- **Don't** use cyan, green, amber, coral, or indigo as decoration or as the only carrier of meaning.
- **Don't** add heavy shadows to resting cards or stack multiple floating planes.
- **Don't** introduce display fonts, terminal-themed body copy, custom scrollbars, or nonstandard form controls for flavor.
- **Don't** animate page-load choreography; motion communicates state and completes within roughly 200ms, except for subtle ambient health cues and a single reduced-motion-safe chart reveal.
- **Don't** let a hosted app inherit, reposition, dismiss, or visually absorb platform safety chrome.
- **Don't** hide or truncate actor, subject, mutability, deadline, or exit when the safety rail wraps on mobile.
- **Don't** announce a per-second countdown through a live region or allow a pending support-session request to be dismissed.
