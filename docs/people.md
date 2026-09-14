---
description: "Onboard people through SSO or private invitations, then manage their roles and access."
---

# People and invitations

Use **Identity → People** to find people, review their roles, and manage access.
Service accounts for deployment automation have their own tab. Use the **More
actions** menu beside a person for password reset, session sign-out, or deletion.
Only applicable actions appear; sign-out and deletion require confirmation.

## People who use SSO

With GitHub, Google, OpenID Connect, or forward authentication configured,
people appear after their first successful sign-in. Below the people list, expand
**How people join** and use **Copy sign-in link**
to share the server's login page. Control who is allowed to authenticate in
the identity provider; the link itself does not grant access.

Group rules and server defaults determine automatically assigned roles.
An administrator can assign a role directly in People or return the account
to group/default governance. The page distinguishes these role sources.
Password changes for accounts without a local password belong in the identity
provider, so People does not offer a local password-reset action for them.

## Invite someone to a local account

When password sign-in is enabled:

1. Expand **How people join** below the people list and choose **Invite person**.
2. Enter their username and choose a role. Viewer is selected initially.
3. Choose **Create invitation**, then copy the private link and send it to the
   intended recipient through a trusted channel. ShinyHub does not send email.
4. The recipient opens the link, reviews the username and role, and chooses
   their own password. ShinyHub signs them in and offers **Open Apps**. If sign-in
   cannot finish, they can use the normal sign-in page with their new password.

The account is created only after acceptance. Each link works once and expires
after seven days. It is a bearer credential: anyone holding it can create the
named account with the assigned role. The link is shown only when created;
copy it before closing the dialog. To replace a lost or expired invitation,
choose **Replace link** in **Pending invitations**. This keeps the username and
role, renews the seven-day expiry, and immediately invalidates the previous link.

Pending invitations are separate from active people. Use **Refresh** to see
newly accepted accounts. Revoke an invitation to immediately invalidate its
link. Links also stop working if the issuing administrator is deleted or no
longer has the Admin role. Disabling password sign-in prevents both creation
and acceptance of local invitations; SSO sign-in remains the onboarding path.

## Choose the right role

| Role | Global permissions |
| --- | --- |
| Viewer | Use apps they have access to. |
| Developer | Create apps and manage their own apps. |
| Operator | Access and manage all apps. |
| Admin | Full access, including people and server settings. |

Individual app membership can grant additional app access or management
permissions. Configure those on the app's **Access** tab. An administrator
cannot change their own role or delete their own account.

## API and audit trail

Human administrators can list, create, replace, and revoke invitations with
`GET /api/user-invitations`, `POST /api/user-invitations`, and
`DELETE /api/user-invitations/{id}`. Use `POST /api/user-invitations/{id}/replace`
to replace a link. Creation accepts `username` and `role` and
returns the invitation metadata plus its one-time secret. Listing never returns
that secret. Service-account credentials cannot manage invitations.

The public preview and acceptance endpoints use POST bodies so the secret does
not enter URL access logs. Invitation links carry the secret in a fragment.
Only a hash is stored; acceptance and account creation are transactional.
Creation, replacement, acceptance, and revocation are recorded in the audit log without
passwords or invitation secrets.

Direct administrator-created local accounts remain available through the
existing CLI/API for administrative recovery and automation. Open public
registration is not enabled by this invitation flow.
