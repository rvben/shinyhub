---
description: "Customize the login page, dashboard, and emails with your own name, logo, and colors. Every white-label field is optional."
---

# Branding (White-Label)

ShinyHub ships a white-label mode that lets operators customize the front door
without touching the core platform. All branding fields are optional. With no
`branding:` block, `/` and `/login` serve the built-in catalog and login page
unchanged.

## YAML config

Add a `branding:` block to `shinyhub.yaml` (see `shinyhub.yaml.example` for the
full commented example):

```yaml
branding:
  site_title: "Example Shiny Platform"
  assets_dir: /etc/shinyhub/assets
  logo: logo.svg               # filename in assets_dir, or an absolute http(s):// URL
  favicon: favicon.ico         # filename in assets_dir, or an absolute http(s):// URL
  theme:
    primary_color: "#0a7d8c"   # CSS hex (#rgb or #rrggbb); sets --brand-primary
  landing_page: landing.html   # filename in assets_dir; replaces the stock catalog at /
  footer_links:
    - { label: "Support",   url: "mailto:support@example.com" }
    - { label: "Community", url: "https://example.com/community" }
```

## Fields

| Field | Description |
|---|---|
| `site_title` | Replaces the `<title>` tag in the SPA shell. On signed-out brand slots it replaces the ShinyHub wordmark when no `logo` is set; in signed-in navigation it appears as a subtitle beneath ShinyHub. |
| `assets_dir` | Directory that backs all local asset references. Required when any field references a local file. |
| `logo` | Brand logo: a filename inside `assets_dir` or an absolute `http(s)://` URL. Replaces the wordmark on the boot splash and login card; signed-in navigation keeps the ShinyHub lockup. |
| `favicon` | Browser-tab icon: a filename inside `assets_dir` or an absolute `http(s)://` URL. Used by the dashboard, login, custom landing fallback, and ShinyHub-owned app status/access pages. |
| `theme.primary_color` | CSS hex color (`#rgb` or `#rrggbb`). Injected as the `--brand-primary` CSS variable. |
| `landing_page` | Filename inside `assets_dir` that replaces the stock app catalog at `/`. `/login` always serves the SPA shell. |
| `footer_links` | List of `{ label, url }` objects. URLs accept `http`, `https`, `mailto`, or an absolute `/path`. |

`assets_dir` is validated at startup: the directory must exist and every
referenced local file must resolve inside it (a symlink-aware containment
check).

### Browser-tab identity

ShinyHub-owned pages use the configured `favicon` consistently, including app
starting, deploying, stopped, crashed, access-denied, and first-deploy pages.
A custom `landing_page` may declare its own `<link rel="icon">`; when it does
not, browsers fall back to the configured icon through `/favicon.ico`.

Running apps keep a favicon declared by their own HTML. When an app does not
declare one, ShinyHub uses the app icon selected in its configuration (emoji or
uploaded image), which keeps several open app tabs distinguishable. An app with
no icon falls back to the platform favicon.

If `site_title` or `logo` replaces the stock identity but no `favicon` is set,
ShinyHub deliberately serves no stock fallback. This prevents the Orbit Hub
mark from leaking into an otherwise white-labelled browser tab.

### Brand slots

A brand slot is anywhere ShinyHub shows product or instance identity: the
sidebar, mobile top bar, boot splash, and login card. The two signed-out slots
use the operator identity; the two signed-in slots remain ShinyHub-first so an
external authentication proxy cannot bypass product identification.

On the boot splash and login card, the first configured value wins:

1. `logo` - rendered as an image, with `site_title` (or `ShinyHub`) as its alt text.
2. `site_title` - rendered as the signed-out identity text.
3. The stock ShinyHub wordmark.

The login card matters most: signed out, it is the only chrome a visitor sees.
A logo sized around 40px tall reads well there; wider lockups are clamped to the
card width.

In the signed-in sidebar and mobile top bar, the ShinyHub wordmark remains the
primary identity. When `site_title` is set, it appears directly beneath the
wordmark as the hub subtitle. The compact desktop rail uses the standalone
ShinyHub mark.

### What branding does not replace

The About dialog, reached through **About ShinyHub** in the sidebar footer,
names ShinyHub and its version, and branding never replaces it. It is the only place a signed-in
operator can read which software and which release they are running, so it has
to survive a full white-label: it is what makes a bug report actionable and what
tells whoever inherits the server what it is. It also reports which app runtimes
the host can start, which is the fastest answer to "why did my R app fail to
deploy".

It sits behind the login and behind a click, where the audience is people who
run the platform rather than people who visit it. The explicit trigger also
keeps the product name discoverable for users who signed in at an external
authentication proxy and never saw ShinyHub's login page.

## Environment overrides

Each scalar field can be set or overridden via an environment variable. The
`footer_links` list has no env override and must be set in YAML.

| Env var | Config field |
|---|---|
| `SHINYHUB_BRANDING_SITE_TITLE` | `branding.site_title` |
| `SHINYHUB_BRANDING_ASSETS_DIR` | `branding.assets_dir` |
| `SHINYHUB_BRANDING_LOGO` | `branding.logo` |
| `SHINYHUB_BRANDING_FAVICON` | `branding.favicon` |
| `SHINYHUB_BRANDING_PRIMARY_COLOR` | `branding.theme.primary_color` |
| `SHINYHUB_BRANDING_LANDING_PAGE` | `branding.landing_page` |

## Asset serving

Local files registered via `logo` and `favicon` are served from an explicit
allow-list at `/branding/<basename>`. The asset handler accepts only a bare
basename (no subdirectory segments) and looks it up in the map, so path
traversal and symlink tricks are blocked at the handler level.

Operator landing pages should reference these assets with the full `/branding/`
prefix:

```html
<img src="/branding/logo.svg" alt="Logo">
```

Relative paths in operator HTML resolve against `/`, not `/branding/`, so the
prefix must be explicit (or add a `<base href="/branding/">` element).

### Remotely hosted images

When `logo` or `favicon` is an absolute `http(s)://` URL, its origin is added to
the control-plane Content-Security-Policy `img-src` list automatically, so the
browser will load it. Only `img-src` is widened, and only with the scheme, host
and port of the images you configured; script, style and connect sources are
untouched. Nothing is added for local assets, which are same-origin already.

The origin is allowed, not the exact path, because asset CDNs redirect and a CSP
path source is matched against the pre-redirect URL only.

This applies to the SPA shell. An operator `landing_page` is served under a
separate policy that does not widen `img-src`, so host landing-page images
locally via `assets_dir`.

The `landing_page` file is served directly at `/` (replacing the stock catalog)
and is NOT exposed under `/branding/`. It is served as trusted same-origin
platform HTML. Only trusted operators should author it; it is not sandboxed.

## Endpoints

| Endpoint | Auth | Description |
|---|---|---|
| `GET /.shinyhub/branding.json` | None (always public) | Returns the active branding object, or `{}` when branding is not configured. |
| `GET /.shinyhub/apps.json` | Optional | Anonymous: public apps only. Admin/operator: all apps. Other authenticated users: apps visible to them (public, shared, owned, or member). Returns minimal `{slug, name, visibility}` objects. Identity is resolved from the browser session cookie only; callers presenting only an `Authorization` header are treated as anonymous. |

Some reverse proxies block dot-prefixed paths. Ensure requests to `/.shinyhub/`
pass through to ShinyHub unmodified.
