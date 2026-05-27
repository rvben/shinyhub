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
| `site_title` | Replaces the `<title>` tag in the SPA shell. |
| `assets_dir` | Directory that backs all local asset references. Required when any field references a local file. |
| `logo` | Brand logo: a filename inside `assets_dir` or an absolute `http(s)://` URL. |
| `favicon` | Favicon: a filename inside `assets_dir` or an absolute `http(s)://` URL. |
| `theme.primary_color` | CSS hex color (`#rgb` or `#rrggbb`). Injected as the `--brand-primary` CSS variable. |
| `landing_page` | Filename inside `assets_dir` that replaces the stock app catalog at `/`. `/login` always serves the SPA shell. |
| `footer_links` | List of `{ label, url }` objects. URLs accept `http`, `https`, `mailto`, or an absolute `/path`. |

`assets_dir` is validated at startup: the directory must exist and every
referenced local file must resolve inside it (a symlink-aware containment
check).

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
