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
| `site_title` | Replaces the `<title>` tag in the SPA shell, and the ShinyHub wordmark in every brand slot when no `logo` is set. |
| `assets_dir` | Directory that backs all local asset references. Required when any field references a local file. |
| `logo` | Brand logo: a filename inside `assets_dir` or an absolute `http(s)://` URL. Replaces the wordmark in every brand slot, including the login card. |
| `favicon` | Favicon: a filename inside `assets_dir` or an absolute `http(s)://` URL. |
| `theme.primary_color` | CSS hex color (`#rgb` or `#rrggbb`). Injected as the `--brand-primary` CSS variable. |
| `landing_page` | Filename inside `assets_dir` that replaces the stock app catalog at `/`. `/login` always serves the SPA shell. |
| `footer_links` | List of `{ label, url }` objects. URLs accept `http`, `https`, `mailto`, or an absolute `/path`. |

`assets_dir` is validated at startup: the directory must exist and every
referenced local file must resolve inside it (a symlink-aware containment
check).

### Brand slots

A brand slot is anywhere ShinyHub shows its own identity: the sidebar, the
mobile top bar, the boot splash, and the login card. All four take the same
value, so one `logo` (or one `site_title`) brands the whole product.

Per slot, the first of these that is set wins:

1. `logo` - rendered as an image, with `site_title` (or `ShinyHub`) as its alt text.
2. `site_title` - rendered as text beside the mark.
3. The stock ShinyHub wordmark.

The login card matters most: signed out, it is the only chrome a visitor sees.
A logo sized around 40px tall reads well there; wider lockups are clamped to the
card width.

### What branding does not replace

The About dialog, reached from the sidebar footer, names ShinyHub and its
version, and branding never replaces it. It is the only place a signed-in
operator can read which software and which release they are running, so it has
to survive a full white-label: it is what makes a bug report actionable and what
tells whoever inherits the server what it is. It also reports which app runtimes
the host can start, which is the fastest answer to "why did my R app fail to
deploy".

It sits behind the login and behind a click, where the audience is people who
run the platform rather than people who visit it. The trigger reads "About", so
an anonymous visitor sees only your brand and a signed-in one gives nothing away
until they open it.

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
