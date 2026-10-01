---
description: "Opt-in Linux native workers with dedicated per-app users and a separate systemd broker."
---

# Native workers with separate app users

Set `runtime.native.broker_socket` to use dedicated per-app Linux identities for
native dependency builds, hooks, replicas and scheduled jobs. The ShinyHub
controller remains unprivileged. A separate root service authenticates the
controller over a Unix socket and launches hardened transient systemd units from
a root-owned app registry. Unknown apps and unavailable protections fail closed;
there is no fallback to the controller UID.

This is opt-in and requires manual provisioning. Leaving `broker_socket` empty
keeps the existing native runtime and its [Landlock dial](isolation.md).
The broker enforces its systemd protections even when the legacy dial is `off`.

## Default and migration policy

This first release keeps the isolated backend opt-in on every platform. An
upgrade does not provision accounts or change the identity of existing apps.
With no broker socket configured, native execution uses the controller UID and
is suitable only for trusted app code. Startup logs identify this as
`trusted_shared_uid`; the isolated backend logs `isolated_systemd` after its
preflight succeeds. Configuring a socket selects isolation explicitly: a missing
broker, unsupported host, invalid policy or failed storage preparation prevents
startup. There is no automatic downgrade.

The intended future default is isolated native execution for **new, supported
Linux installations**. Changing that default requires automatic provisioning
and retirement of app identities/storage, verified scheduling/autoscaling and
hibernation/upgrade flows, representative app/cache performance measurements,
and a supported policy for shared data and unusual dependencies. Those are
release prerequisites; this first implementation still needs manual provisioning
and refuses cross-app shared mounts. Existing installations will need an explicit
migration; trusted native execution will remain an explicit compatibility option.

For a gradual pilot, one host can run separate ShinyHub instances using the two
backends. Give them different controller UIDs, private groups, databases, storage
and listening ports. Keep legacy app processes outside the isolated controller's
UID and app groups. Within one instance, `broker_socket` applies to all native
tiers; it is not a per-app selection.

## Boundary and requirements

Use Linux with systemd as PID 1, cgroup v2, and systemd support for `LoadCredential`,
`ProtectProc`, transient services and freeze/thaw (tested on Ubuntu 24.04,
systemd 255). The broker binary, policy, state and runtime directories and their
ancestors must be root-owned and not writable by other users. The controller
must use a non-root account.

Each app has a dedicated UID and private primary group, with no supplementary
groups. Apps cannot read the controller's private database, configuration,
environment or another app's private storage or process environment. Units have
no capabilities, `NoNewPrivileges`, private temporary directories/devices,
`ProtectSystem=strict`, read-only cgroups and writable access only to their own
registered bundle, data and cache trees. CPU/memory limits and a 512-task limit
apply to the entire unit, including descendants. App arguments and environment
are delivered through a systemd credential, rather than public unit properties.

The boundary is **per app**. All its versions, builds, jobs and replicas share
one UID and can affect each other. Host networking, loopback and the kernel are
shared; world-readable host files remain readable. This backend does not replace
containers or VMs for hostile tenants, does not solve browser-origin isolation,
and does not restrict app network egress. Use a separate app origin as described
in [configuration](configuration.md).

The controller joins every private app group to upload, back up and restore
ordinary app files. It retains authority over all registered apps. The broker
prepares app trees with controller ownership, private app groups, `0770`
directories and `0660` files (`0770` for executables). Symlinks are not followed;
special files, foreign-owned files and controller-owned hardlinks from other
groups are refused. Apps that deliberately make their files inaccessible can
still disrupt their own backup or service.

## Provision one app

Stop the controller and **all legacy native app/job/build processes** before
migration. Old workers still have the controller UID and could authenticate to
the new broker. Migrate on a test host first, with a backup. Do not reuse app UIDs
when removing apps; remove their workers and storage before retiring an identity.

The example below assumes the controller already uses UID/GID `22000`, the app
slug is `example`, and its database app ID is `1`. Substitute the actual values;
an app ID is not an OS UID. Create an app record before enabling this backend,
or register a new app's ID before its first deploy. Do not run untrusted bundles
to discover their IDs.

```bash
sudo useradd --uid 22001 --user-group --no-create-home \
  --home-dir /nonexistent --shell /usr/sbin/nologin shinyhub-example
sudo usermod --append --groups shinyhub-example shinyhub

# Common ancestors are traversable, but app directories stay private.
sudo install -d -o shinyhub -g shinyhub -m 0711 \
  /var/lib/shinyhub /var/lib/shinyhub/apps \
  /var/lib/shinyhub/app-data /var/lib/shinyhub/app-cache
sudo install -d -o shinyhub -g shinyhub-example -m 0750 \
  /var/lib/shinyhub/apps/example
sudo install -d -o shinyhub -g shinyhub-example -m 0770 \
  /var/lib/shinyhub/apps/example/versions \
  /var/lib/shinyhub/app-data/example /var/lib/shinyhub/app-cache/example
sudo install -d -o shinyhub -g shinyhub -m 0700 /var/lib/shinyhub/control
```

Move the existing database, its WAL/SHM files and snapshots into the private
`control/` directory **while stopped**, and update `database.dsn`. Keep database
and secret files `0600` and all control-only directories `0700`. Do not make an
existing directory containing secrets publicly traversable as a shortcut to
exposing app storage. Keep config and `auth.secret_file` outside app trees.
Preflight refuses an existing config/secret file that an app UID can read through
its Unix file and directory permissions. Keep control files free of ACL grants
to app identities as well. Do not rotate `auth.secret` as part of this move; it also encrypts stored app
secrets. Preserve it as described in [secret rotation](secret-rotation.md).

Install the matching Linux broker release (or build with
`make build-native-broker`), the reference unit and the policy example:

```bash
sudo install -m 0755 shinyhub-native-broker /usr/local/libexec/shinyhub-native-broker
sudo install -m 0644 deploy/systemd/shinyhub-native-broker.service \
  /etc/systemd/system/shinyhub-native-broker.service
sudo install -o root -g root -m 0600 deploy/systemd/native-broker.json.example \
  /etc/shinyhub/native-broker.json
```

Edit the policy to use the real IDs and absolute paths. Every app needs three
existing, non-overlapping roots. The policy's `bundle_root` must be
`<storage.apps_dir>/<slug>/versions`; data and cache roots must be
`<storage.app_data_dir>/<slug>` and `<storage.app_cache_dir>/<slug>`. Root paths
cannot contain symlinks, whitespace or systemd specifiers. Restart the broker
after policy changes and the controller after changing group membership.
The broker does not create users, allocate IDs or automatically register apps.

Configure the controller:

```yaml
database:
  dsn: /var/lib/shinyhub/control/shinyhub.db
storage:
  apps_dir: /var/lib/shinyhub/apps
  app_data_dir: /var/lib/shinyhub/app-data
  app_cache_dir: /var/lib/shinyhub/app-cache
runtime:
  mode: native
  native:
    broker_socket: /run/shinyhub-native-broker/control.sock
```

The environment override is `SHINYHUB_RUNTIME_NATIVE_BROKER_SOCKET`.
Add this controller service drop-in:

```ini
[Unit]
Requires=shinyhub-native-broker.service
After=shinyhub-native-broker.service
```

Then reload and restart:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now shinyhub-native-broker
sudo systemctl restart shinyhub
```

Keep `NoNewPrivileges=true` on the controller. Do not grant it sudo or install
a setuid helper. The root broker connects to systemd on its behalf; app workers
cannot connect to the controller-only socket. Shared-data symlinks do not grant
permission to another app's private group. Such shares require a separate,
explicitly designed read-access policy; this first backend does not grant them.
System tools and interpreters must be readable/executable outside home
directories, because worker units use `ProtectHome=yes`. Managed Python, uv,
renv and home caches are redirected into the app's registered cache root.

## Lifecycle and operations

Before app code runs, the broker persists a unit identity and the worker waits
for the controller's startup acknowledgement. Closing the guard without an
acknowledgement aborts the launch. After a controller crash, the existing
ownership lease can delay re-adoption until its TTL expires (30 seconds by
default); the worker continues running during that interval. Publication/consumer lock descriptors retain
their kernel open-file identity through the handoff, so a controller crash does
not release a live worker's locks. Recovery uses labelled unit records, boot IDs
and process birth times rather than adopting arbitrary PIDs. Stopping a worker
stops its whole cgroup, including detached descendants; broker unavailability is
not reported as proof of worker exit.

Normal deploys, hooks, rollback, live CPU/memory changes and cold hibernation use
the existing flows. When snapshots are enabled, the broker freezes the unit and
attempts `memory.reclaim`; insufficient reclaim thaws it and uses the existing
cold-stop fallback. Swap and kernel support are still necessary for substantial
anonymous-memory reclaim. Process metrics can use the existing observer, but
recovered app-specific OTel environment values cannot be read across the UID
boundary; platform defaults apply on recovery.

Broker state is operational metadata, not app data. Preserve it through broker
restarts and controller handoffs. Do not delete it while workers may be alive.
Final records are retained for idempotent wait/stop responses and excluded from
active inventory; monitor its disk usage. A broker upgrade does not stop existing
units. Verify worker shutdown before removing policy entries, users or storage.
Stop all app workers through the API before an offline restore; stopping the
controller alone leaves workers running when `server.shutdown_apps=adopt`.
Backups of ShinyHub do not provision OS users or this root policy on a new host;
restore those separately with the same IDs before starting apps.
Startup prepares every registered app's private storage before launching workers,
including idle apps. Restore keeps preserved storage copies controller-private.

## Verification

The ordinary Go suites do not require root or systemd. The live suite is gated
and runs only in a disposable Linux VM, provisioned with
`internal/nativebroker/testdata/provision.py`; that fixture refuses existing
users or storage and installs the hardened reference service. When copying only
the fixture script to the VM, pass the copied reference unit's path as its first
argument. Cross-compile the `internal/process` test binary and run it as
`shiso-control` with `NATIVE_BROKER_TEST_SOCKET` set to the fixture socket.
The separate `internal/nativebroker` normalization suite requires root and
`NATIVE_BROKER_TEST_ROOT=1` in that disposable fixture.

The checks exercise private app identity/storage, actual Shiny startup, guard
abort, descriptor-lock lifetime, client replacement, isolated builds, memory
limits and detached-descendant shutdown. Test real deploys and recovery with
synthetic data before enabling this on an existing fleet.
