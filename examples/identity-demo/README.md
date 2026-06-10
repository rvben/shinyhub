# identity-demo

A Shiny for Python app that demonstrates ShinyHub identity forwarding. It
verifies the signed `X-Shinyhub-Identity-Token` JWT on every request and
displays the caller's username, platform role, and group memberships. An
admins-only panel appears when the verified role is `admin`.

## Deploy

```bash
shinyhub login --host https://your-shinyhub.example.com
shinyhub deploy examples/identity-demo --slug identity-demo
```

After deploy, the app is live at `https://your-shinyhub.example.com/app/identity-demo/`.

## What to observe

- **Signed in:** The app shows your username, role, and groups read from the
  verified JWT. If your role is `admin`, the admins-only panel is visible.
- **Anonymous (private window or logged-out):** The app shows the anonymous
  visitor message. For a public app, logged-out visitors reach this branch;
  for a private or shared app, ShinyHub redirects unauthenticated requests to
  the login page before they reach the app.

**Note:** Set the app's access level to `private` or `shared` (Configuration
tab) to require sign-in. With `public` access, logged-out visitors reach the
app and see the anonymous message.

## Further reading

See [`docs/identity.md`](../../docs/identity.md) for the full header reference,
JWT claims, trust model, and verification recipes in Python and R.
