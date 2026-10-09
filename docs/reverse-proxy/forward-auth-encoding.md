---
description: "The UTF-8 contract for forward-auth identity headers, including Starlette and FastAPI integrations."
---

# Forward-auth identity header encoding

The values of `auth.forward_auth.user_header`, `name_header`, `email_header`,
and `groups_header` must contain **UTF-8 bytes**. This is ShinyHub's contract
for these custom headers; HTTP headers do not provide a universal UTF-8
encoding. Configure the auth service and reverse proxy to preserve the bytes
without transcoding. ASCII values already satisfy this contract.

ShinyHub validates every value of each configured identity header, including
repeated values, from a trusted proxy after verifying its shared secret and before
looking up or creating accounts or reconciling groups. Validation applies when
the proxy asserts a non-empty username and ShinyHub processes the request as a
forward-auth identity. Anonymous, signed-out, and public favicon requests bypass
identity processing.

The following rules apply to assertions processed as forward-auth identities:

| Invalid UTF-8 in | Behavior |
|---|---|
| Username | Refuse the request with HTTP 400; no account lookup or creation. |
| Any groups header value, including repeated values | Refuse the request with HTTP 400; no account creation or role/group reconciliation. |
| Display name | Ignore the incoming name and keep the stored display name. |
| Email | Omit email from this request's app headers and identity token, without changing persisted email. No fallback to a previously stored email. |

For valid repeated username, name, or email headers, the first value is used,
preserving the existing behavior. Any malformed value invalidates the field.
Repeated groups headers are combined into one comma-separated membership list.

A malformed groups assertion is not treated as a partial or empty membership
snapshot. Rejected requests do not reach apps using previously stored roles.
A missing groups header still follows `require_groups_header`: when enabled,
it produces HTTP 403; otherwise it means no groups and triggers reconciliation.

Encoding failures generate a WARN immediately and at most once every five
minutes per peer IP and configured header while failures continue. Diagnostics
identify the field and peer without including identity values or credentials.
Tracking is limited to 1,024 peer/header pairs. If that limit is reached, new
pairs share a five-minute warning budget until space becomes available; those
warnings include `peer_tracking_limited=true`. The response identifies the kind
of invalid header without echoing its value.

## Starlette and FastAPI auth services

Starlette's string-based response-header API encodes values as Latin-1.
Passing a Unicode name directly can raise an encoding error for characters
outside Latin-1, and characters within Latin-1 can be sent with the wrong
bytes. When using that API, wrap each identity string as follows:

```python
headers = {
    "X-Forwarded-User": username.encode("utf-8").decode("latin-1"),
    "X-Forwarded-Name": display_name.encode("utf-8").decode("latin-1"),
    "X-Forwarded-Email": email.encode("utf-8").decode("latin-1"),
    "X-Forwarded-Groups": ",".join(groups).encode("utf-8").decode("latin-1"),
}
```

The intermediate Latin-1 string is a reversible carrier: Starlette encodes it
back into the original UTF-8 bytes. Use it only at the header API boundary;
keep application strings as ordinary Unicode. Identity values must also obey
HTTP header restrictions; do not include newlines or other forbidden control
characters. Group names in this comma-separated format cannot contain commas.

## Limits and existing data

ShinyHub does not guess an encoding, accept Latin-1 as a fallback, percent-decode
values, or normalize Unicode. A literal `%20` remains `%20`. Usernames and
groups retain their existing exact matching behavior after whitespace trimming.

Valid UTF-8 does not prove that text was encoded correctly upstream: previously
mojibaked text can itself be valid UTF-8. Validation also does not repair old
invalid database values or merge accounts created under different encodings.
Correct the proxy first and review affected accounts separately.

For the UTF-8 headers sent from ShinyHub to apps, including Python decoding
and the preferred signed-token path, see [identity header encoding](../identity.md#header-encoding).
