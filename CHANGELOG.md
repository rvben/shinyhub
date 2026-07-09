# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/).








































































## [0.10.2](https://github.com/rvben/shinyhub/compare/v0.10.1...v0.10.2) - 2026-07-09

### Fixed

- **deploy**: let sandboxed builds provision uv-managed Python interpreters ([c5b3c29](https://github.com/rvben/shinyhub/commit/c5b3c29b50b2a64d6f3f00b7916324988208a707))

## [0.10.1](https://github.com/rvben/shinyhub/compare/v0.10.0...v0.10.1) - 2026-07-08

### Added

- **proxy**: shed new elastic workers under host memory pressure ([3ed9e65](https://github.com/rvben/shinyhub/commit/3ed9e65547d39629387b69f6e257d6f9bf8e7ccf))

### Fixed

- **api**: resolve inherited isolation and merged state in the worker guard math ([e87c318](https://github.com/rvben/shinyhub/commit/e87c318aad9e2a5c39f70586dfb93f894df2948d))
- **api**: compute the worker memory-guard math against the post-patch limit ([3c51a4d](https://github.com/rvben/shinyhub/commit/3c51a4d8d2910353420c831c805afe1782eca2e8))
- **schedules**: retry a transient first-fire failure once, guarded against double-fire ([7e0433b](https://github.com/rvben/shinyhub/commit/7e0433bad5d0f9bb62972fd6df5857c42f34f542))

## [0.10.0](https://github.com/rvben/shinyhub/compare/v0.9.6...v0.10.0) - 2026-07-08

### Added

- **identity**: forward the per-app role to apps (X-Shinyhub-App-Role) ([0a412ed](https://github.com/rvben/shinyhub/commit/0a412ed506db3f48cafe7b24274d7c1a179085e3))
- **api**: opt-in audit-log access for operators ([e8ff30d](https://github.com/rvben/shinyhub/commit/e8ff30d755e16fee21c18dddf272626e0c90111a))
- **auth**: session revocation via per-user token epoch ([971d680](https://github.com/rvben/shinyhub/commit/971d680643e981465b1dc7e0d59f7bfe57e586a4))
- **auth**: API token expiry, last-used tracking, and admin inventory ([085921b](https://github.com/rvben/shinyhub/commit/085921bd51d8188e0c913c12189853ef9c7e961b))
- **api**: app ownership transfer ([09ed240](https://github.com/rvben/shinyhub/commit/09ed240e6d8f50ce22862107cc98de9988d229aa))
- **auth**: scope the deploy token to an app allowlist and warn on admin role ([4dc2423](https://github.com/rvben/shinyhub/commit/4dc242350eff780aa07e7a4a6fd0d79d9a91d8a6))

### Fixed

- **api**: advertise can_read_audit on the SPA session-login response ([4f78e5a](https://github.com/rvben/shinyhub/commit/4f78e5aaa3a0331556b6f813ebf934cf0e1a88e3))
- **deploy**: make project-mode launches sync dependencies on off-host tiers ([0a8b274](https://github.com/rvben/shinyhub/commit/0a8b27420b85db49b9911dc0b44cb3581c055697))
- **access**: correct grant and visibility messaging that pushed apps toward over-exposure ([78d1af8](https://github.com/rvben/shinyhub/commit/78d1af8b41c3aac0a07f42bb39a3369deb163462))

## [0.9.6](https://github.com/rvben/shinyhub/compare/v0.9.5...v0.9.6) - 2026-07-08

### Added

- **oauth**: add nonce defense-in-depth to OIDC login/callback ([6f87cc6](https://github.com/rvben/shinyhub/commit/6f87cc622a84abcdce4bde87392c7205f54a6f7a))
- **cli**: --force flag on restore to bypass the running-server guard ([1c154b2](https://github.com/rvben/shinyhub/commit/1c154b2b12aed63cde90b3b6e46919e43c8e3ee0))

### Fixed

- **deploy**: sandbox the dependency-build and post-deploy-hook exec phases ([10bf4ca](https://github.com/rvben/shinyhub/commit/10bf4cad9e0a503f672e86e1db954130264118d9))
- **lifecycle**: avoid persisting false hibernated state on stop failure ([5f2ab31](https://github.com/rvben/shinyhub/commit/5f2ab317b85286f8a18a96868ea9b77fe8e5e267))
- **ui**: replace rollback alert() with an accessible toast ([3969f08](https://github.com/rvben/shinyhub/commit/3969f0855082ab0734646d511c0e0d507c737f97))
- **ui**: guard against double-submit on delete/revoke/create/restart ([ed35370](https://github.com/rvben/shinyhub/commit/ed353708975ed1229850e0736738b27d3f93f10c))
- **ui**: close the new-token modal on Escape ([cd9f687](https://github.com/rvben/shinyhub/commit/cd9f6877f21d2d13770c15a83569c0a309f2a585))
- **ui**: distinguish rate-limited login and guard against double-submit ([dee359a](https://github.com/rvben/shinyhub/commit/dee359ac8fef20fc41b119174a3bed33312ee4e1))
- **ui**: give the schedules table responsive overflow on mobile ([1442463](https://github.com/rvben/shinyhub/commit/1442463bbef2dc49d29460c2508a84353af8ce1e))
- **ui**: harden schedule and shared-data handlers against network/auth failures ([cc0b5ec](https://github.com/rvben/shinyhub/commit/cc0b5ec0ab674d9d338d512d2d75a0b9789df519))
- **ui**: wire onError into the background metrics poll ([eaa3a9f](https://github.com/rvben/shinyhub/commit/eaa3a9f355221534200776bd98c8cfdfd87b4499))
- **backup**: refuse to restore into a live server unless forced ([2271116](https://github.com/rvben/shinyhub/commit/22711161307f02378132f0ba920d2e6c6fe6a194))
- **backup**: verify archive integrity before reporting a backup written ([c084f12](https://github.com/rvben/shinyhub/commit/c084f12fb6ebacced1496646ab9acab10edf0328))
- **ui**: warn before session-expiry logout discards unsaved settings edits ([ebf658a](https://github.com/rvben/shinyhub/commit/ebf658a32601a018f49af4247fe227c8a2d2c031))
- **ui**: surface app-detail load failures instead of blanking the panel ([5bb874d](https://github.com/rvben/shinyhub/commit/5bb874d060cbbeff9ec8d9d4b53170cdd4eaa5b4))
- **terraform**: remove cross-tenant Secrets Manager grant on the app task role ([e72954b](https://github.com/rvben/shinyhub/commit/e72954b6dac12d9d6be46dd28f25a59f50bee8b8))
- **db**: serialize Postgres Migrate() with a session advisory lock ([78678fa](https://github.com/rvben/shinyhub/commit/78678fa48838fda8a630af793279fc641cef9f84))
- **process**: bound StopReplica's post-SIGKILL wait ([8554552](https://github.com/rvben/shinyhub/commit/855455275e435f9cc29e1149a60421b1b6a38d9e))
- **proxytrust**: read the rightmost forwarded value, not the leftmost ([6762aeb](https://github.com/rvben/shinyhub/commit/6762aebaa6155545474da2119c413cd3cd45b318))
- **proxy**: don't hibernate elastic pools with live worker connections ([3087c0f](https://github.com/rvben/shinyhub/commit/3087c0fd93c2ddd0c415d705253f9594dc4831ab))
- **worker**: pin worker-agent mTLS peer to control-plane identity ([72c804f](https://github.com/rvben/shinyhub/commit/72c804f859b5e7f0d74ff029c248236ba0c8fb4f))

### Performance

- **lifecycle**: batch watchdog reconcile queries instead of per-app fetches ([ab03d25](https://github.com/rvben/shinyhub/commit/ab03d25662a6f90c345e900d263a694d12eaff96))

## [0.9.5](https://github.com/rvben/shinyhub/compare/v0.9.4...v0.9.5) - 2026-07-04

### Added

- **auth**: SSO-only mode - disable local password login ([fd7453f](https://github.com/rvben/shinyhub/commit/fd7453f14ec78f5fbb7c4b37f9d52e1222230d54))
- **identity**: forward the user's display name to apps ([10b5623](https://github.com/rvben/shinyhub/commit/10b56234111cef3e096792d09bffe6f98b1efec4))
- **proxy,ui**: warn when an app serves HTTP but no WebSocket connects ([399cef3](https://github.com/rvben/shinyhub/commit/399cef34095998514b4192bc5751bf9cc1eb8f02))
- **worker**: agent self-fences (StopAll) on a fenced heartbeat ([c022cfe](https://github.com/rvben/shinyhub/commit/c022cfe37d9654358f898a3a23d6b27008727c63))
- **fargate**: pre-create per-app S3 Files directory before first mount ([14470cc](https://github.com/rvben/shinyhub/commit/14470cc77af62f239b965cc6ad70c400e36c6ef5))
- **worker**: add replicaServer.StopAll to kill and forget all tracked replicas ([40e7659](https://github.com/rvben/shinyhub/commit/40e7659f6cf17110b34687347322a32c0cbafe1e))
- **worker**: heartbeat client sends the worker incarnation ([e42d21b](https://github.com/rvben/shinyhub/commit/e42d21b5df018915269e33851b664a6bccff04ec))
- **worker**: reap bumps incarnation via the down-monitor + register returns it ([0c7b6fc](https://github.com/rvben/shinyhub/commit/0c7b6fc05fe4d542f5d8b51e1964f9cfca4f3f79))
- **worker**: fence a rejoining worker with a stale incarnation ([28b366f](https://github.com/rvben/shinyhub/commit/28b366f4081bb68d9908ee215cd015ffd46e06e2))
- **fargate**: mount per-app S3 Files volume into task definitions ([b4e63d9](https://github.com/rvben/shinyhub/commit/b4e63d904eef6117cf5bc19664f80cc95f9099a9))
- **config**: add runtime.fargate.s3files managed durable-data backend ([fb0e502](https://github.com/rvben/shinyhub/commit/fb0e5021f9be111eb16a97db3dd6d7f64c64705c))
- **worker-api**: carry incarnation + fenced on register/heartbeat ([6d95f8b](https://github.com/rvben/shinyhub/commit/6d95f8b18b522c2e9eacf98bdebd70342a049029))
- **db**: ReapWorker bumps worker incarnation; revoke bumps too ([101cfe8](https://github.com/rvben/shinyhub/commit/101cfe83286ca8c9ab71cd768dcba40e780f48b7))
- **db**: add worker incarnation column + scans ([20c4168](https://github.com/rvben/shinyhub/commit/20c4168cbd9384dd22499c8e619b3cfc7d1861e1))
- **cli**: add apps set --ephemeral-data-ok ([600ff8e](https://github.com/rvben/shinyhub/commit/600ff8eeb4fc9d16bde345a8e0dd788f294c1b47))
- **api**: block deploy and data push of data-using apps on ephemeral tiers ([09336a7](https://github.com/rvben/shinyhub/commit/09336a726ef03bae9e1791fb8551dcf3bed5f524))
- **db**: add ephemeral_data_ack app column ([bc7356b](https://github.com/rvben/shinyhub/commit/bc7356b389f56d940745c949a1f85e66e5facff2))
- **runtime**: report per-tier app-data durability ([384014a](https://github.com/rvben/shinyhub/commit/384014a943ffc9ddd61079d0e2c231a97a194b24))
- **deploy**: detect persistent-data use and decide ephemeral-tier blocking ([587fa0a](https://github.com/rvben/shinyhub/commit/587fa0a61642573486bc99b4a9a64bfb6394a16c))
- **api,cli,ui**: paginate schedule runs + apps list, completing T2-10 ([5d10af8](https://github.com/rvben/shinyhub/commit/5d10af833c6a0ff908e74b7390f79cc9cc061312))
- **api,cli,ui**: paginate members, env, data, users lists (T2-10) ([c624276](https://github.com/rvben/shinyhub/commit/c6242768e1bba9156c0e2820a8dee50c27e268e5))
- **api,cli,ui**: paginate schedule status/ls, share ls, group-list (T2-10) ([a381196](https://github.com/rvben/shinyhub/commit/a381196f3a8125c342caedbd8eda8a8cde2ae6b3))
- **cli**: getPaginatedListWithExtra surfaces envelope extra keys ([dd48745](https://github.com/rvben/shinyhub/commit/dd48745fca607ca0d59ae2375899eba576c9bd48))
- **api,cli,ui**: paginate tokens list via standard envelope (T2-10) ([21ec939](https://github.com/rvben/shinyhub/commit/21ec939c9e06de7c66b0920378dd99fa93985d80))
- **api,cli,ui**: paginate deployments list via standard envelope (T2-10) ([a0dbe28](https://github.com/rvben/shinyhub/commit/a0dbe28e6be782cab0517b0c0fc4fe1a51222df6))
- **api,cli**: add writeList + renderServerList pagination helpers ([9d2a265](https://github.com/rvben/shinyhub/commit/9d2a26599859d62914b51c3f69fe522d46a11fb5))

### Fixed

- **ui**: show OAuth/SSO login buttons only when the provider is configured ([8c700a2](https://github.com/rvben/shinyhub/commit/8c700a2112ef69781cc37318281d3081e5162997))
- **identity**: resolve email + display name on the /app proxy path ([5e1d30f](https://github.com/rvben/shinyhub/commit/5e1d30f56974d5551f3516989f8fef402327601f))
- **proxy**: fire WS-ready on Hijack, not WriteHeader(101) ([10b358f](https://github.com/rvben/shinyhub/commit/10b358fb9eee3fd1b8c5860c6e0cf75ea54a3890))
- **db**: renumber ephemeral_data_ack migration to 044 ([b301868](https://github.com/rvben/shinyhub/commit/b30186854214d90bc60bec6affe8f768d5bc5913))
- **fargate**: warn when s3files access point disables per-app isolation; fix migration comment ([c3d092a](https://github.com/rvben/shinyhub/commit/c3d092a2e7a6db594a98d3c1b72709387228e162))
- **api**: guard tier/placement changes, rollback, and restart against ephemeral data loss ([f7ed9e4](https://github.com/rvben/shinyhub/commit/f7ed9e4bea5fa2d010ec2ebb54a627d1b8abbd88))
- **fargate**: key per-app S3 Files directory on app id, not slug ([981ca52](https://github.com/rvben/shinyhub/commit/981ca5266d53fb8565ea5c782424f78bfa79adcf))
- **process**: name Delegate= in native cgroup-degradation warning ([1075881](https://github.com/rvben/shinyhub/commit/1075881c54ea739d6915c46b139a24fabd099582))
- **cli**: keep schedule runs default page at 200 ([c3bfb3e](https://github.com/rvben/shinyhub/commit/c3bfb3e512d04a9c8577be48852aff70fec6cad6))
- **cli**: reject negative --limit/--offset on server-paginated lists ([142ac51](https://github.com/rvben/shinyhub/commit/142ac51eaa0c59410a5432c71c8045463568986e))

## [0.9.4](https://github.com/rvben/shinyhub/compare/v0.9.3...v0.9.4) - 2026-07-03

### Added

- **ui**: light theme with System/Light/Dark preference ([8236623](https://github.com/rvben/shinyhub/commit/8236623027aa4b7468fa7231ba00bb9a102c7962))
- **ui**: API tokens management page (create, reveal-once, list, revoke) ([0c20570](https://github.com/rvben/shinyhub/commit/0c20570f360735a7ff2adf8604e774452f667142))

### Fixed

- **ui**: darken light-theme --cyan to WCAG AA; rename inset triplet ([1a1009c](https://github.com/rvben/shinyhub/commit/1a1009c8aca2c8b4f76c6e554e9a6a6294ad9223))
- **ui**: resolve WCAG A/AA accessibility violations in the dashboard ([3864a28](https://github.com/rvben/shinyhub/commit/3864a287f8c58ba053f0f9d9e7e928a87908cefd))
- **cli**: drop OutputFields the server never emits ([d935453](https://github.com/rvben/shinyhub/commit/d93545365c815cc1d86e02295ceb6b4c2c98361a))

## [0.9.3](https://github.com/rvben/shinyhub/compare/v0.9.2...v0.9.3) - 2026-07-02

### Added

- **ui**: WAI-ARIA keyboard navigation for the app-detail tablist ([573c88f](https://github.com/rvben/shinyhub/commit/573c88f9bb2f485c7d9397d9576d8d18ef888803))
- **api**: rate-limit failed bearer/token auth per client IP ([c71f769](https://github.com/rvben/shinyhub/commit/c71f769413a4bf7a5b6d8fe63aab1b1af9a88f80))

### Fixed

- **api**: make auth-failure limiter check-and-consume atomic ([24222f6](https://github.com/rvben/shinyhub/commit/24222f6c296a0e8689161623cfc5819f50a00461))
- **ui**: manual activation for tablist keyboard nav so arrows keep working ([9d35dd1](https://github.com/rvben/shinyhub/commit/9d35dd16a002fb1c165adf83ac675efc5743a4f6))
- **api**: never throttle a valid token on the auth-failure limiter ([a4a1019](https://github.com/rvben/shinyhub/commit/a4a1019c51fa4a3f9e29f9efb6fe2a60ac5b049d))

### Performance

- **proxy**: move elastic accounting off the global write lock ([1615f38](https://github.com/rvben/shinyhub/commit/1615f38746b0214f5bef698c4b68714e804edcb6))

## [0.9.2](https://github.com/rvben/shinyhub/compare/v0.9.1...v0.9.2) - 2026-07-02

### Added

- shinyhub migrate-backend for SQLite -> Postgres migration ([72c1ea7](https://github.com/rvben/shinyhub/commit/72c1ea760cce82ac33c78768091abb925c1b78ea))
- shinyhub rotate-secret to re-encrypt at-rest secrets on auth.secret change ([3b07fab](https://github.com/rvben/shinyhub/commit/3b07fabaf55871f28888d68423cf0c4c080d5240))
- **lifecycle**: audit-log app crashes ([75a04a7](https://github.com/rvben/shinyhub/commit/75a04a732510d064e3ae5e12fd5c885a72ffcb6f))
- **proxy**: worker isolation dial - native per_session/grouped session isolation ([7a4772a](https://github.com/rvben/shinyhub/commit/7a4772a2d11eb936f9813891a6277372cf75aa4d))
- **ui**: worker isolation controls and host-capacity helper line ([d4aae82](https://github.com/rvben/shinyhub/commit/d4aae827092d878d346a570eebd3e9ec80dd16d7))
- **proxy**: wire SetPoolMode at deploy/recovery/patch sites; skip elastic replica boot (Task 13) ([f0c8a57](https://github.com/rvben/shinyhub/commit/f0c8a577887d4ea8b11da74394c76ca7d00cfa31))
- **lifecycle**: wire elastic worker spawn and terminate (Task 12) ([d352a84](https://github.com/rvben/shinyhub/commit/d352a84b952a457bf5911b33391eb85a38c3348d))
- **proxy**: demand-driven elastic routing for grouped/per_session ([982c565](https://github.com/rvben/shinyhub/commit/982c5659323ab731e0da9a3cbeaca24b67208ad8))
- **proxy**: write-locked worker reservation and per-client accounting ([f6aa1ef](https://github.com/rvben/shinyhub/commit/f6aa1ef78eb8bc9010a4589d31d42dfd96369d22))
- **proxy**: stable signed client-id cookie ([efc1a76](https://github.com/rvben/shinyhub/commit/efc1a760e2ce929fedbf6206891f0207a940d692))
- **proxy**: add elastic pool storage fields and SetPoolMode for worker isolation ([7abd142](https://github.com/rvben/shinyhub/commit/7abd142d22bcf9a4893e8dd008dffbb2be8265f8))
- **proxy**: pure worker-allocation decision policy ([71be70e](https://github.com/rvben/shinyhub/commit/71be70e33efc3093812997a32c81e8ac5bf72894))
- **cli**: apps set isolation flags + manifest [app.worker] block ([d32f6d9](https://github.com/rvben/shinyhub/commit/d32f6d98fdbe7adea1e1d97fe566304610b6dc02))
- **api**: parse and validate worker isolation fields on PATCH ([664f1b7](https://github.com/rvben/shinyhub/commit/664f1b72f691ead076128d155da58a399f404599))
- **db**: persist worker isolation settings via patch and manifest apply ([9803599](https://github.com/rvben/shinyhub/commit/98035999d8af2f7897a3f0504624caee343a56f0))
- **config**: worker isolation fleet default + resolver ([7a1ce04](https://github.com/rvben/shinyhub/commit/7a1ce04d167513d0e0840b1708f642f472fde112))
- **config**: worker isolation settings type and shared validator ([9024b96](https://github.com/rvben/shinyhub/commit/9024b96979b9588c22d16a3388e7602c02fa5874))
- **db**: add worker isolation columns (migration 040) ([dece116](https://github.com/rvben/shinyhub/commit/dece116a07e44d991f8d0c54493983fdfbcf6a14))

### Fixed

- refuse non-empty migration targets; clear route-error view on recovery ([d687f33](https://github.com/rvben/shinyhub/commit/d687f33e1a62265a1299fc42d5d0809f2166f31b))
- **db**: coerce integer booleans in backend migration + rotation scan ([12c53fa](https://github.com/rvben/shinyhub/commit/12c53fa5f6b1217a44ce141c17f7d764b70f74d8))
- **proxy**: bind client-id cookie to the user instead of clearing on logout ([1b3241f](https://github.com/rvben/shinyhub/commit/1b3241fde9a48a9108cc936e168457a933f62c5a))
- **ui**: surface apps-grid load failures instead of a silent empty grid ([65f81dc](https://github.com/rvben/shinyhub/commit/65f81dc63650f131bca9ced64725abfb329e1969))
- **auth**: cap absolute session lifetime so SSO role changes take effect ([6e455ac](https://github.com/rvben/shinyhub/commit/6e455ac347795639414eff2045a0ea0b31c2da68))
- **process**: isolate Docker app containers on an ICC-disabled network ([fe25454](https://github.com/rvben/shinyhub/commit/fe254547a550bba00e0b47961ee68f7d2a33b627))
- **api**: JSON error envelope everywhere; stop leaking raw errors ([d4fb290](https://github.com/rvben/shinyhub/commit/d4fb290bd14ec37d083726e2ae9b154ad8cd32cb))
- **ui**: global error boundary so a view throw can't blank the shell ([08aae2e](https://github.com/rvben/shinyhub/commit/08aae2ef2e18876c9f42c0bc736edc1f819351be))
- **db**: widen autoscale_target to double precision on Postgres ([20c3016](https://github.com/rvben/shinyhub/commit/20c3016e4ab1d5c3025349c1c5ed7d4822a20a15))
- **proxy**: close worker-isolation client-id cookie hijack ([3a2c420](https://github.com/rvben/shinyhub/commit/3a2c420de1585ab1241ef42f3311bc53b125c15c))
- **proxy**: reclaim not-yet-connected elastic worker slots; test-sentinel + deploy-skip log ([aad1c92](https://github.com/rvben/shinyhub/commit/aad1c92b0b77cc7fa8dd59c239dfda4a8af01074))
- **lifecycle**: resolve isolation mode before elastic skip guard at wake/warm/recovery ([3ee3acd](https://github.com/rvben/shinyhub/commit/3ee3acd71d16b5f99a493ea947aca6b9f5c5086c))
- **lifecycle**: cancel max_session_lifetime timer on early terminate ([d076e49](https://github.com/rvben/shinyhub/commit/d076e49f997618d2207f29663d56ec657136f13a))
- **proxy**: route elastic by server-side client binding; close terminate-race window ([8f11848](https://github.com/rvben/shinyhub/commit/8f11848c63b499e8bfd32978963fd481e529013f))
- **proxy**: floor liveConns to avoid double-close timer leak ([43bc263](https://github.com/rvben/shinyhub/commit/43bc263cbc1dd5d3c17a8c4bded94ac473010a0f))
- **proxy**: set Secure on client-id cookie; correct doc + tamper test ([21bd0c2](https://github.com/rvben/shinyhub/commit/21bd0c286ddbe5ce43be8684b0cbc3c903b8dd8a))

### Performance

- **api**: batch the dashboard metrics poll instead of 3 queries per card ([0c81ed0](https://github.com/rvben/shinyhub/commit/0c81ed02a38c12717cb6e6d5d0153f1d31d21db9))
- **proxy**: skip elastic sticky-cookie refresh when it already matches ([27d04ea](https://github.com/rvben/shinyhub/commit/27d04ea551fea0a72442cecc9590cd4285449108))
- **process**: read log tail backward instead of scanning the whole file ([55e76a2](https://github.com/rvben/shinyhub/commit/55e76a2eb2843fcaa7316c9c2d6af7ae2b8c37a1))
- **recovery**: parallelize worker inventory and bound its context ([6a4acdd](https://github.com/rvben/shinyhub/commit/6a4acdd82401a28efc5eba16cac3df7a9cc46f5a))
- **ui**: revalidation cache policy for static assets ([6fd55f7](https://github.com/rvben/shinyhub/commit/6fd55f749389376779473620009bd5e9ed211333))
- **db**: index deployments(app_id, created_at DESC, id DESC) ([79c757c](https://github.com/rvben/shinyhub/commit/79c757cab4e430d68baab486973ddc9a8e5468af))

## [0.9.1](https://github.com/rvben/shinyhub/compare/v0.9.0...v0.9.1) - 2026-07-01

### Added

- **runtime**: native process isolation dial (Landlock, standard tier) ([9e9c0f3](https://github.com/rvben/shinyhub/commit/9e9c0f319000c20e6c0443cafdaca8f1e91fdfa6))
- **identity**: persist SSO email so X-Shinyhub-Email works for native sessions ([ec97374](https://github.com/rvben/shinyhub/commit/ec97374a0a98e9512e711ec9ce3a1f0477ae0ba4))
- **identity**: forward user email (X-Shinyhub-Email + email claim) ([b4c1348](https://github.com/rvben/shinyhub/commit/b4c1348a6378b28c112e69af4d988af5c4bf9f64))
- **identity**: shinyhub-identity Python + R client helper packages ([b0ef309](https://github.com/rvben/shinyhub/commit/b0ef309b7d771f5ea7a92df54ba346d424c1890d))
- **fleet**: declarative autoscale in the fleet manifest [app.config] ([52e0602](https://github.com/rvben/shinyhub/commit/52e0602e7dd143267430d1e729cfdd3cb9c6c0dd))
- **metrics**: per-app session and admission-ceiling gauges ([3f28c6c](https://github.com/rvben/shinyhub/commit/3f28c6cc0e8ec97f2c77828da43ed44c2d370097))
- **autoscale**: declarative per-app autoscale in shinyhub.toml [app] ([ea50cd7](https://github.com/rvben/shinyhub/commit/ea50cd72228d3b3e0af1b863a6510bfe70a5dcdc))

### Fixed

- **runtime**: normalize sandbox paths to absolute and fall back TMPDIR ([61fd482](https://github.com/rvben/shinyhub/commit/61fd48269e7e47acd1b7ea7f03fffd78d9eb7146))
- **identity**: resolve GitHub private-email primary for X-Shinyhub-Email ([ea83605](https://github.com/rvben/shinyhub/commit/ea8360524170250547985ef7c3260d6ec771bdda))
- **config**: strict top-level config - reject unknown, empty, multi-doc ([4277290](https://github.com/rvben/shinyhub/commit/42772908b7895bebc2aae0034a9db7fdefa27321))
- **identity**: correct the R helper missing-exp test construction ([a7239e8](https://github.com/rvben/shinyhub/commit/a7239e83a3962088dab7be9164d27fc9bd9762c6))

## [0.9.0](https://github.com/rvben/shinyhub/compare/v0.8.29...v0.9.0) - 2026-06-30

### Added

- **ui**: surface stale schedules in the admin fleet-health banner ([c857386](https://github.com/rvben/shinyhub/commit/c8573866cbb2ca6da1ceed4317d1b8818fc2e208))
- **cli**: add schedule status command for fleet data-freshness ([eee56b1](https://github.com/rvben/shinyhub/commit/eee56b1b3cdd8dd2cde30412dc093baeeebc10e2))
- **api**: add admin schedule-status endpoint with cron-aware stale flag ([9132a89](https://github.com/rvben/shinyhub/commit/9132a896bb20cdbe1bf2380ee065e57d210e15f9))
- **metrics**: add DB-backed schedule last-success collector ([dbd6090](https://github.com/rvben/shinyhub/commit/dbd60903680dec07e56b266dead07aacd3963a82))
- **jobs**: record terminal scheduled-run outcomes to metrics ([29137b1](https://github.com/rvben/shinyhub/commit/29137b19538e35fb0ce408e03d9e8bb4e1702557))
- **metrics**: add schedule_runs_total counter and Register hook ([375a84a](https://github.com/rvben/shinyhub/commit/375a84acea227533dd5292efed4c5f41b668f0d1))
- **db**: add ScheduleFreshness query and shared timezone resolution ([ac1fd4d](https://github.com/rvben/shinyhub/commit/ac1fd4d57d685d0f8065984af1f2c8b3604d0d69))
- **schedulespec**: add cron-aware IsStale freshness policy ([1825d20](https://github.com/rvben/shinyhub/commit/1825d20415966bd4ff1ddeabf2fecacf83e53083))
- **cli**: parallelise fleet apply with bounded --concurrency (default 3) ([6c99095](https://github.com/rvben/shinyhub/commit/6c990950bf429504ddcddf4c279a730335ede996))
- **cli**: document fleet apply --concurrency in the schema ([b6845f9](https://github.com/rvben/shinyhub/commit/b6845f962c4e69b4f7a1d4780d76a4ec05eeed9c))
- **cli**: run fleet apply deploys under a bounded worker pool ([6e61995](https://github.com/rvben/shinyhub/commit/6e61995a41625a796d5def7d354598e3d0872e97))
- **cli**: add --concurrency flag to fleet apply ([d7068c2](https://github.com/rvben/shinyhub/commit/d7068c2c6301b9741555b766713cea16007d7936))
- **deploy**: bound the environment build with build_timeout_seconds + progress logging ([0f33508](https://github.com/rvben/shinyhub/commit/0f3350829c091f5fccc363ebc9b965db1fdcf29e))
- **localrun**: cancel in-flight build on run cancellation; detect build cancel ([8bffbf2](https://github.com/rvben/shinyhub/commit/8bffbf2e6c9b97e177cd20835a9503b4976c09a3))
- **cli**: document build_timeout_seconds in the deploy schema ([64eed4e](https://github.com/rvben/shinyhub/commit/64eed4e8dad2556a032d7fc94abb6749cee8d9e1))
- **deploy**: bound the environment build with build_timeout_seconds and log progress ([cd916d9](https://github.com/rvben/shinyhub/commit/cd916d975fd5752d6c8d189650aa6079d6060d24))
- **deploy**: add per-app build_timeout_seconds manifest setting ([b924c5f](https://github.com/rvben/shinyhub/commit/b924c5fafd63c240fc4cdeef78c144b67b93a4bf))
- **limits**: per-app CPU/memory limits in shinyhub.toml [app] ([09cd7ac](https://github.com/rvben/shinyhub/commit/09cd7ac44200d6f762b75062272af4ff9eddfd24))
- **cli**: structured deploy failure_kind + per-attempt reasons in fleet apply ([fb76845](https://github.com/rvben/shinyhub/commit/fb768458789b649ad69a09c6aea10ec2d98095df))
- **cli**: document fleet apply failure_kind in the schema ([dee3638](https://github.com/rvben/shinyhub/commit/dee36387cccb0192f8c442b0b63fa4da6127ae82))
- **cli**: surface failure_kind in fleet apply report and JSON ([88b5c83](https://github.com/rvben/shinyhub/commit/88b5c83bc0cb82936af0ea603ed1cddf4ac91bfe))
- **cli**: record per-attempt deploy failure kind in fleet apply ([8178f79](https://github.com/rvben/shinyhub/commit/8178f798883d90c58beaed7b4d100e0f8277426a))
- **api**: emit structured failure_kind in deploy failure response ([f35c99b](https://github.com/rvben/shinyhub/commit/f35c99b7e16e9b1ba5fcc126d9021d85337e0016))
- **deployfail**: classify deploy errors into failure kinds ([e5b9156](https://github.com/rvben/shinyhub/commit/e5b915647690fe376fa85cdf72ce91ad66e84d5c))
- **deployfail**: add deploy failure kind vocabulary ([af88429](https://github.com/rvben/shinyhub/commit/af88429e48b9fdfe2bb7850d5b212dc3470a9c91))
- **ui**: hide internal app state from the viewer Launchpad ([2c75129](https://github.com/rvben/shinyhub/commit/2c75129fb1999129e3958b930d8c6f8bdee2e272))
- **ui**: admin preview of the viewer home ([0c47cc4](https://github.com/rvben/shinyhub/commit/0c47cc4e23edc1438d5b1a4c06de4ea98c55dc41))
- **branding**: auth-aware root + stable /home alias ([242eeac](https://github.com/rvben/shinyhub/commit/242eeacfbc9f60c854ca6329d65a5745080560aa))
- **ui**: per-app uploadable icons with monogram fallback ([3e22116](https://github.com/rvben/shinyhub/commit/3e2211696a5b6bb2aecf022e0c2c02d79b3c876b))
- **ui**: viewer Launchpad home + per-app description ([3f16a34](https://github.com/rvben/shinyhub/commit/3f16a34beaca08f1b0487a91bb06210faae0e4e9))
- **ui**: operator Overview dashboard home ([77ae8c1](https://github.com/rvben/shinyhub/commit/77ae8c104b9e559a1150eae1f9c185dbcd8e98aa))

### Fixed

- **cli**: write deploy log tail as one block so parallel apply does not interleave it ([597a195](https://github.com/rvben/shinyhub/commit/597a195bf1618ec48c49f18c89452a307d75f665))
- **cli**: classify 4xx deploy rejections as bundle_invalid, not server_error ([4b32cbe](https://github.com/rvben/shinyhub/commit/4b32cbe8164c72b162bba5874724b136150ebc69))
- **cli**: attribute top-level failure_kind only to deploy failures ([cb6e560](https://github.com/rvben/shinyhub/commit/cb6e560d171a12d593eba88de8a0724fb848683f))
- **worker**: close bundle-cache pull dedup TOCTOU between stat and lock ([d8df05a](https://github.com/rvben/shinyhub/commit/d8df05a1095c55ec6c2ebf903a3afe02f8fc5cf2))

## [0.8.29](https://github.com/rvben/shinyhub/compare/v0.8.28...v0.8.29) - 2026-06-22

### Added

- **security**: drop CSP 'unsafe-inline', allow branding inline by hash ([cb9bc1b](https://github.com/rvben/shinyhub/commit/cb9bc1b6b25174c52ad6b74fa9850c97d1ca3cda))

## [0.8.28](https://github.com/rvben/shinyhub/compare/v0.8.27...v0.8.28) - 2026-06-22

### Added

- **auth**: share the login rate limiter across instances on Postgres ([c57104c](https://github.com/rvben/shinyhub/commit/c57104c4b4ef4202a96fbc64869e0affe59007ae))
- **backup**: support Postgres backends via pg_dump/pg_restore ([5825a20](https://github.com/rvben/shinyhub/commit/5825a20b4e6c81cf164467e2f5df6159dfeb9c69))
- **process**: enforce native-mode CPU limits via cgroup v2 cpu.max ([16ef3be](https://github.com/rvben/shinyhub/commit/16ef3be889282945d168fa34fc40107e20cc411e))

## [0.8.27](https://github.com/rvben/shinyhub/compare/v0.8.26...v0.8.27) - 2026-06-22

### Added

- **process**: enforce native-mode memory limits via cgroup v2 ([283755a](https://github.com/rvben/shinyhub/commit/283755a142b8e9bb92094b466aa019cf8061e662))

## [0.8.26](https://github.com/rvben/shinyhub/compare/v0.8.25...v0.8.26) - 2026-06-20

### Added

- **db**: bound audit-log and schedule-run growth with retention pruning ([018cd75](https://github.com/rvben/shinyhub/commit/018cd7501a9487163599a9137f79a3203ba3191e))
- **observability**: add pprof, apps_crashed gauge, audit-error counter ([1c0f304](https://github.com/rvben/shinyhub/commit/1c0f304ad0663608c9ded5f5d921b53a872be0ac))
- **db**: refuse to start against a database from a newer build ([fc3c207](https://github.com/rvben/shinyhub/commit/fc3c2074b8c88ea5596bf3489f9e8507e62e077b))

### Fixed

- **observability**: only expose pprof on a loopback metrics listener ([96b2360](https://github.com/rvben/shinyhub/commit/96b23605666149f0714f4ad4ca8fac8a3df8e305))
- **config**: load the maintenance block from YAML, not only env vars ([4583898](https://github.com/rvben/shinyhub/commit/45838983de5ed0b90ed9839013c9c4b3adc5e2d2))
- startup robustness and small correctness hardening ([1804cf3](https://github.com/rvben/shinyhub/commit/1804cf3f8ffa3d97c6a87c57ac0fab43af396282))
- **users**: reject deleting a user who still owns apps with a clear 409 ([dd28a71](https://github.com/rvben/shinyhub/commit/dd28a714663f4f133603dd7a9ef57bda3dd7f0f3))
- **process**: recover exit-monitor panics; configurable stop grace ([b68257f](https://github.com/rvben/shinyhub/commit/b68257fb988c17b5595f1c2ac858095afcbb0a7d))
- **security**: add Secure to sticky cookie, HSTS, and Permissions-Policy ([98a5a8a](https://github.com/rvben/shinyhub/commit/98a5a8a201da14f8b0a693cbcb1d11b21f3bb6b9))
- **deploy**: prune old versions synchronously under the deploy lock ([5ad8c1f](https://github.com/rvben/shinyhub/commit/5ad8c1fad88dfc3d61eaeb8ba80a7004c963e972))
- **api**: cap request body size to prevent memory exhaustion ([5a66116](https://github.com/rvben/shinyhub/commit/5a661160a846e7e225ac242c8370c7861790322b))
- **server**: bound slow clients and hung backends with connection timeouts ([0dab39b](https://github.com/rvben/shinyhub/commit/0dab39b3f53e14b312a1d0e3f14a9e9744234751))

## [0.8.25](https://github.com/rvben/shinyhub/compare/v0.8.24...v0.8.25) - 2026-06-20

### Added

- **cli**: add 'shinyhub run' local dev server ([6d00af1](https://github.com/rvben/shinyhub/commit/6d00af12fad3132a2741fd01a9628ecdaada5be9))
- **cli**: add shinyhub run local dev server command ([886ceb4](https://github.com/rvben/shinyhub/commit/886ceb464ac7134a05889ff7553acda0b643f2b7))
- **localrun**: file-watch restart fallback for manifest-command apps ([6dcef81](https://github.com/rvben/shinyhub/commit/6dcef8197abfb04c7193722b2be3447733fb4c27))
- **localrun**: foreground runner with readiness, --check, signal handling ([20d5aaa](https://github.com/rvben/shinyhub/commit/20d5aaaba21996f87c47985e7f887a5290f924aa))
- **deploy**: resolve inferred python/r launch commands with reload ([ecfe39f](https://github.com/rvben/shinyhub/commit/ecfe39f37be95f062a8a4f0d2c68555eef059f1a))
- **deploy**: add LaunchPlan seam (override + manifest-command paths) ([5eb1511](https://github.com/rvben/shinyhub/commit/5eb151184421e0dfc4e0ab553ecb2d99a2a6d9a6))
- **bundle**: exclude .shinyhub-run from deploy bundles ([7b2a358](https://github.com/rvben/shinyhub/commit/7b2a3587a61d916183e3625397c0484fe06e98a3))

### Fixed

- **localrun**: recompute readiness URL from re-resolved plan on restart ([524d343](https://github.com/rvben/shinyhub/commit/524d343c795da86435be3bfd3963153bfcacf4f4))
- **localrun**: prevent subprocess orphan, reserved-env leak, relative data-dir, and watcher debounce race ([f7327bb](https://github.com/rvben/shinyhub/commit/f7327bbd5795a6863ee617364bad72dd6938b576))
- **deploy**: make ensure-project dep-prep step nonfatal and remove unused DataDir option ([7514435](https://github.com/rvben/shinyhub/commit/75144359169d4d9667caf10d0bdcc7a56d3b756a))
- **proxy**: prevent control-plane startup panic adopting remote_docker replicas ([3a6f85f](https://github.com/rvben/shinyhub/commit/3a6f85f4a013841606755a5ba5915f19eeb08cbd))

## [0.8.24](https://github.com/rvben/shinyhub/compare/v0.8.23...v0.8.24) - 2026-06-18

### Added

- **ui**: rework app status colors and labels ([9379efb](https://github.com/rvben/shinyhub/commit/9379efbd56003c3df25fc5a60240ad57bb62463e))

### Fixed

- **ui**: show a crash-looped never-deployed app as Failed on its detail page ([b904062](https://github.com/rvben/shinyhub/commit/b90406297adcc7f4ede43669b3bc812276549085))

## [0.8.23](https://github.com/rvben/shinyhub/compare/v0.8.22...v0.8.23) - 2026-06-18

### Added

- **api**: batch app metrics into one request so the dashboard loads at once ([86e6323](https://github.com/rvben/shinyhub/commit/86e632388cf82faba2fcd02c636cbf3422d76723))

## [0.8.22](https://github.com/rvben/shinyhub/compare/v0.8.21...v0.8.22) - 2026-06-18

### Added

- **proxy**: serve warm-wake resumes inline instead of via the loading page ([3e1e26d](https://github.com/rvben/shinyhub/commit/3e1e26de7f58a32accca34648d0bdfe29439b491))

## [0.8.21](https://github.com/rvben/shinyhub/compare/v0.8.20...v0.8.21) - 2026-06-18

### Added

- **ui**: show instance count and sum CPU/RAM across replicas on the app card ([650a635](https://github.com/rvben/shinyhub/commit/650a63516ef0dd1cafded730678913774e904500))

### Fixed

- **ui**: stop app card buttons shifting when CPU/RAM appears on start ([bb974fc](https://github.com/rvben/shinyhub/commit/bb974fc9e77139def314eb99e302188b45d3c7d1))

## [0.8.20](https://github.com/rvben/shinyhub/compare/v0.8.19...v0.8.20) - 2026-06-18

### Added

- **lifecycle**: surface crashed apps with a reason and one-click recovery ([7ace24d](https://github.com/rvben/shinyhub/commit/7ace24df228f9adb28b70c9e5042120d27000038))

## [0.8.19](https://github.com/rvben/shinyhub/compare/v0.8.18...v0.8.19) - 2026-06-17

### Fixed

- **warm-wake**: make warm-restore produce a working warm state across restarts ([a268c39](https://github.com/rvben/shinyhub/commit/a268c39c602967f6de51677a2a8c2f0c51ec3d71))

## [0.8.18](https://github.com/rvben/shinyhub/compare/v0.8.17...v0.8.18) - 2026-06-17

### Added

- **warm-wake**: restore warm apps on startup so they survive a restart ([4472cc7](https://github.com/rvben/shinyhub/commit/4472cc79a79e9489b9bbb05dd20eb35425a2072b))

## [0.8.17](https://github.com/rvben/shinyhub/compare/v0.8.16...v0.8.17) - 2026-06-17

### Fixed

- **warm-wake**: re-adopt frozen replicas warm after a restart instead of reaping them ([b491814](https://github.com/rvben/shinyhub/commit/b491814a76759c8974813a999634efaade58f53b))
- **warm-wake**: re-register the per-app cgroup when a replica is adopted after a restart ([fe43b01](https://github.com/rvben/shinyhub/commit/fe43b012d334492b525365a046dece0db77ffd24))

## [0.8.16](https://github.com/rvben/shinyhub/compare/v0.8.15...v0.8.16) - 2026-06-17

### Added

- **deploy**: add per-app startup_timeout_seconds readiness deadline ([f274eaf](https://github.com/rvben/shinyhub/commit/f274eaf1c68a5abe6277fb33b0e32a78d5cd69b2))
- **fleet**: surface the failing app's log tail on fleet apply ([eedfa74](https://github.com/rvben/shinyhub/commit/eedfa74af68c041e7099350254867b0bbc63bdd7))
- **fleet**: default manifest to fleet.toml with shinyhub-fleet.toml fallback ([ea1f95c](https://github.com/rvben/shinyhub/commit/ea1f95cd115498dc4278e529cd88abea67ccea5f))

### Fixed

- **schedule**: re-fire a run_on_register first-fire interrupted by a restart ([850d1bc](https://github.com/rvben/shinyhub/commit/850d1bcdade494a5b3c1a37f9a71ad8a99d0f34a))
- **schedule**: report exit_code null until a run reaches a terminal state ([3efccaa](https://github.com/rvben/shinyhub/commit/3efccaa2e2d55f06938195a3597b6f05b8e6af98))

## [0.8.15](https://github.com/rvben/shinyhub/compare/v0.8.14...v0.8.15) - 2026-06-17

### Fixed

- **deploy**: fail health check fast when an app crashes on startup ([4c31761](https://github.com/rvben/shinyhub/commit/4c31761d41342a694674e45271dfd750021e5ca4))

## [0.8.14](https://github.com/rvben/shinyhub/compare/v0.8.13...v0.8.14) - 2026-06-17

### Added

- **metrics**: add in-memory historical app metrics with Overview sparklines ([60682a5](https://github.com/rvben/shinyhub/commit/60682a51841a2e5891151264540dc6fd451ac3a9))

### Fixed

- **deploy**: ensure pydantic for shiny apps (shinychat optional-import workaround) ([4bc5ef4](https://github.com/rvben/shinyhub/commit/4bc5ef49c42d7d955c0a8eebbc97b6aa0431c35b))

## [0.8.13](https://github.com/rvben/shinyhub/compare/v0.8.12...v0.8.13) - 2026-06-16

### Added

- **lifecycle**: reap orphaned frozen warm containers on recovery ([01899c1](https://github.com/rvben/shinyhub/commit/01899c16355d643af5a70fd67d4973b4e53232b6))
- **lifecycle**: thaw warm-pool replicas on scale-up instead of cold-boot ([dd0888e](https://github.com/rvben/shinyhub/commit/dd0888e8af6ba8a23545ac17d910c1de71d0318d))

### Fixed

- **ui**: keep app-card status badge live and drop title link underline ([5d2bc33](https://github.com/rvben/shinyhub/commit/5d2bc334793636aafd2157a2b54c0369811d7a83))

## [0.8.12](https://github.com/rvben/shinyhub/compare/v0.8.11...v0.8.12) - 2026-06-16

### Added

- **runtime**: enable native warm-wake in buildRuntime ([c3e67a7](https://github.com/rvben/shinyhub/commit/c3e67a74f292128b0a7e69218729a5f469df0494))
- **process**: native warm-wake via per-app cgroup reclaim ([f8dfd5d](https://github.com/rvben/shinyhub/commit/f8dfd5dcc9960f1f8d5c7f29230d41ac6b06acd2))
- **lifecycle**: evict oldest suspended replicas over max_suspended ([6176c9b](https://github.com/rvben/shinyhub/commit/6176c9b44dff2da1033a9efc33715a1ac9a54c78))
- **server**: enable docker warm-wake from snapshot config ([1717396](https://github.com/rvben/shinyhub/commit/1717396c0eb7e63ce25acfdce29ba57146a04b9c))
- **process**: implement DockerRuntime Snapshotter (freeze + reclaim) ([c44a2e0](https://github.com/rvben/shinyhub/commit/c44a2e0c64e0c0c23fc592d6c394f1f6956da439))
- **process**: add cgroup v2 memory.reclaim helpers (linux) ([7caebc6](https://github.com/rvben/shinyhub/commit/7caebc6b2153edb24c0b98e793846bdf18286a05))
- **process**: add dockerClient pause/unpause ([4a604b4](https://github.com/rvben/shinyhub/commit/4a604b48bca1006e8f67ff8e94ce7e882c1e7407))
- **config**: add runtime.docker.snapshot config (warm-wake) ([ef736a0](https://github.com/rvben/shinyhub/commit/ef736a05c24a1c1584100451b381d1a98310490b))
- **process**: add reclaim-success threshold helper for warm-wake ([48efa27](https://github.com/rvben/shinyhub/commit/48efa27a008626af0375857d3446d01179551ded))
- **server**: wire resume executor into the lifecycle watcher ([7cb42b0](https://github.com/rvben/shinyhub/commit/7cb42b014e2a5cbe6240351aee7c1fbfdde8ac64))
- **lifecycle**: suspend on hibernate and resume on wake with fallbacks ([655e94d](https://github.com/rvben/shinyhub/commit/655e94d84fcb1d574d02c717d14d872d06f37bd4))
- **deploy**: add ResumeReplica with abbreviated readiness probe ([dfdefb8](https://github.com/rvben/shinyhub/commit/dfdefb8d21b63d05373c8a264e5f7747b9146370))
- **process**: add Manager.Suspend/Resume snapshot fan-out ([e2f81b7](https://github.com/rvben/shinyhub/commit/e2f81b7c0d87a1c6a507fba5c9fc2d1417b0633d))
- **process**: add Snapshotter capability and suspended status ([13f0f2e](https://github.com/rvben/shinyhub/commit/13f0f2e0d1f71c5d9aa377c8f5dc460ce2e39bcb))
- **deploy**: freeze requirements.txt apps at deploy to stop cold-start drift ([a878844](https://github.com/rvben/shinyhub/commit/a8788445efa14e335e84b724bf533c9b9193685a))
- **auth**: capture forward-auth display name from proxy name header ([6505558](https://github.com/rvben/shinyhub/commit/650555830a2de4056127dbf45207518a15836444))
- **ui**: sidebar identity card and editable profile modal ([ec3f1d7](https://github.com/rvben/shinyhub/commit/ec3f1d743001dcc3adeb71eacdc07ba751195485))
- **api**: self-service profile endpoint and IdP-managed names ([cfea65a](https://github.com/rvben/shinyhub/commit/cfea65a4842ebe75d636da87dfb89312f9fa1242))
- **db**: add user display_name column and store methods ([ce072ec](https://github.com/rvben/shinyhub/commit/ce072ec9e0889b1188fd9d6f0dba9167d2258589))

### Fixed

- **process**: make ensureDelegatedBase idempotent across calls ([0138795](https://github.com/rvben/shinyhub/commit/013879514cccd7c16081371cdfec189ea1854ec2))
- **deploy**: launch project-mode apps with uv run --frozen --no-sync ([15e7403](https://github.com/rvben/shinyhub/commit/15e740379e80028825835ce5e3c2f20d4dace234))
- **proxy**: recover from a dead/hibernated replica instead of 502 (single-node) ([d28d12c](https://github.com/rvben/shinyhub/commit/d28d12c6d27a57dafec90b531c440de1ce5d3eed))
- **process**: stable suspend baseline, unconditional unfreeze on stop, EINTR retry ([a177125](https://github.com/rvben/shinyhub/commit/a177125547f132eeeb3ed70336d13660c8d95fdd))
- **process**: unfreeze a suspended replica before stopping it ([4eece02](https://github.com/rvben/shinyhub/commit/4eece02873416b41ad1172dcc4b610d66ed2edcc))
- **process**: guard suspend/resume status writes with handle re-check ([6457551](https://github.com/rvben/shinyhub/commit/645755139824683137dbca578e773b5a860daecf))
- **ui**: replace the login-form flash on load with a boot splash ([afddd9b](https://github.com/rvben/shinyhub/commit/afddd9b3ae33b52ab0d9ae6d0f79753b66121f82))

## [0.8.11](https://github.com/rvben/shinyhub/compare/v0.8.10...v0.8.11) - 2026-06-14

### Added

- **ui**: human-friendly deployment versions (vN) instead of epoch timestamps ([e80eae9](https://github.com/rvben/shinyhub/commit/e80eae99e3ac34bda73fbf5771f79c647288dcc6))

## [0.8.10](https://github.com/rvben/shinyhub/compare/v0.8.9...v0.8.10) - 2026-06-14

### Added

- **ui**: dashboard-grade app-detail header with real metric tiles ([ace0063](https://github.com/rvben/shinyhub/commit/ace0063ff2293f2302b2bcc1d37bd53b6fc80365))

### Fixed

- **ui**: make the mobile app-detail tab strip excellent ([a4e38a6](https://github.com/rvben/shinyhub/commit/a4e38a648e5e3ccea9fe296618f981114e80e9f2))

## [0.8.9](https://github.com/rvben/shinyhub/compare/v0.8.8...v0.8.9) - 2026-06-14

### Added

- **ui**: global left sidebar navigation with project-grouped app switcher ([1414870](https://github.com/rvben/shinyhub/commit/14148702876d8d3c3421ec6946756b933d21354e))
- **ui**: folder-tab navigation and settings-page polish on app detail ([455bd9c](https://github.com/rvben/shinyhub/commit/455bd9cfb92a0c555d9d0bd3c86ab271c5a5622c))
- **ui**: redesign app settings as GitLab-style settings blocks ([df49b75](https://github.com/rvben/shinyhub/commit/df49b75084c97411ca6f6b9ff1d0bd817b0f081e))
- **ui**: make settings section headers prominent and on-brand ([46b8c91](https://github.com/rvben/shinyhub/commit/46b8c91f4c04481ee10c17c919d8d6e59b8e047b))
- **ui**: dashboard excellence pass (bug fixes, settings IA, explicit-save, responsive) ([b4094fd](https://github.com/rvben/shinyhub/commit/b4094fda83ddc416aec10090a5f7513947fd599f))

## [0.8.8](https://github.com/rvben/shinyhub/compare/v0.8.7...v0.8.8) - 2026-06-13

### Added

- **config**: let backup and restore load config without auth.secret ([3acafd3](https://github.com/rvben/shinyhub/commit/3acafd3e925050065d682817d272ae7f0fef8e9d))
- **auth**: warn when a forward-auth user header arrives from an untrusted peer ([3297071](https://github.com/rvben/shinyhub/commit/3297071720de4d51dc17d3ea8605df712c43d9fb))
- **cli**: add SHINYHUB_CREDENTIALS env var for the client credentials path ([76421c5](https://github.com/rvben/shinyhub/commit/76421c5ceb836a5398a31e45f843972970e46c37))
- **cli**: TTY-aware apps logs default and apps get alias ([e59eec8](https://github.com/rvben/shinyhub/commit/e59eec8f042565a471c051446471fd21e524ecd9))

## [0.8.7](https://github.com/rvben/shinyhub/compare/v0.8.6...v0.8.7) - 2026-06-13

### Added

- **cli**: surface the app log tail inline on a deploy failure ([3481c0a](https://github.com/rvben/shinyhub/commit/3481c0afc782b13cd6753f70b300a1f6738e4488))
- **cli**: add --wait to apps restart and rollback ([da65355](https://github.com/rvben/shinyhub/commit/da653556286852cecf4a6bda56967bb1ddd6209c))
- **cli**: add schedule runs command for run history ([0e50924](https://github.com/rvben/shinyhub/commit/0e509246bf9b9bf5d29bd29d7dcc8150e6f9000b))
- **cli**: add apps metrics command ([cd740f7](https://github.com/rvben/shinyhub/commit/cd740f768c6619787fd7732f8b3ca2f942144158))
- **cli**: add users command group for admin user management ([690f695](https://github.com/rvben/shinyhub/commit/690f695fe5901e33aaae541d5f34a279b8668309))

### Fixed

- **fleet**: apply declared [app.config] on create, not just source ([8b0105a](https://github.com/rvben/shinyhub/commit/8b0105a95ce9f719e2eccad984de923f90e9da16))
- **api**: explain rollback after a failed deploy instead of a bare 409 ([ae16668](https://github.com/rvben/shinyhub/commit/ae1666823c406f128f6a6047a5e43fb0fccdd697))
- **schedule**: record the runtime error in a failed run's log ([394391e](https://github.com/rvben/shinyhub/commit/394391eb02d6ba492e0ae6afb0dd137d37f2aade))
- **cli**: tag replica 0 in apps logs NDJSON output ([7596a63](https://github.com/rvben/shinyhub/commit/7596a634d139459112ee22ab82de6bd418ace741))
- **ui**: declare neverDeployed per card so the apps grid renders ([9bfcd15](https://github.com/rvben/shinyhub/commit/9bfcd159c548ba1ea092f39c07fbaa74d0703fb5))

## [0.8.6](https://github.com/rvben/shinyhub/compare/v0.8.5...v0.8.6) - 2026-06-13

### Added

- **ui**: render a failed-only deploy as "Failed", not "Awaiting deploy" ([a99a1b5](https://github.com/rvben/shinyhub/commit/a99a1b5d8aaa87225a531a50b3832be1a5f7071a))
- **fleet**: include app_url in fleet apply JSON output ([c38aaea](https://github.com/rvben/shinyhub/commit/c38aaead934beb799ebdcb6714a4e2c71516ca2d))
- **fleet**: emit a commented [[app]] template when fleet init finds no apps ([03b5a05](https://github.com/rvben/shinyhub/commit/03b5a059cab7c079e9ec3472629df7c671f58e70))
- **fleet**: add --fail-on-changes alias for fleet plan --detailed-exitcode ([d90ff4a](https://github.com/rvben/shinyhub/commit/d90ff4a19043184c14c7b16a226da31fe942b86d))
- **deploy**: print access level after deploy and document bundle contents ([4c80d30](https://github.com/rvben/shinyhub/commit/4c80d30139e4ea008275ca58dd34afd7de7e29c7))
- **access**: warn when a grant has no effect on a private app ([8f6c92b](https://github.com/rvben/shinyhub/commit/8f6c92b04368b6d1f35e0afa02cfbf0f176dc7f2))
- **cli**: add whoami to show the active login (username, role, server) ([9d28079](https://github.com/rvben/shinyhub/commit/9d2807948ef1091567ed143929dced50389098fc))
- **cli**: document SHINYHUB_HOST/TOKEN/CONFIG in --help and schema ([74a4314](https://github.com/rvben/shinyhub/commit/74a4314b938ecfce57b8e4d61e7c64e4bf9a7756))
- **data**: echo effective destination + size on push and add --dry-run ([a81b239](https://github.com/rvben/shinyhub/commit/a81b23987f0d110d1bd915e67b5283e7bae9d085))
- **env**: nudge a restart when an env change leaves a running app stale ([eafa040](https://github.com/rvben/shinyhub/commit/eafa040464783360ba746caacb5249d418e403b5))
- **server-info,deploy**: report host runtimes and warn before an R deploy that will fail ([a65140c](https://github.com/rvben/shinyhub/commit/a65140c7d24019bfcb44ad154188193136901540))
- **deploy**: persist and surface deployment failure reason ([e14fc47](https://github.com/rvben/shinyhub/commit/e14fc47e8cd5c5a7aa6fee26b10f92e38c12ffb5))
- **idempotency**: idempotent repeat semantics across CLI and API ([b3d2f01](https://github.com/rvben/shinyhub/commit/b3d2f01490caff07f8b5670109ea16fdb5aeb84c))
- **cli**: confirmation_required refusals and login validation without a TTY ([a71c12b](https://github.com/rvben/shinyhub/commit/a71c12ba823f4594ed3075957e244645a26c4ad5))
- **cli**: JSON success envelopes for mutating commands ([5890ffd](https://github.com/rvben/shinyhub/commit/5890ffd47b55a961e7fa92903e280dda4573d75f))
- **cli**: NDJSON streaming mode for log-follow commands ([af9be8c](https://github.com/rvben/shinyhub/commit/af9be8ce0db5e376c51046c9d94fad65083981fe))
- **cli**: fleet status envelope v2 with items and truncation metadata ([58cf9d0](https://github.com/rvben/shinyhub/commit/58cf9d0400864c34bf2b2e7c060651912dd74861))
- **cli**: bounded envelope output for env, data, schedule, and share lists ([891ecb5](https://github.com/rvben/shinyhub/commit/891ecb5052cf8c477d2cb08afabf60616ad5b955))
- **cli**: bounded envelope output for deployments, access, and token lists ([8682b9d](https://github.com/rvben/shinyhub/commit/8682b9d67df1a93a1d2255fe244afa6e47b65e30))
- **cli**: bounded envelope output for apps list ([da3e1df](https://github.com/rvben/shinyhub/commit/da3e1dfff42dd56e688bb8f0aee92165f3a609de))
- **cli**: shared bounded-list rendering with limit/offset/fields ([7fc054a](https://github.com/rvben/shinyhub/commit/7fc054a2b746a1d332f91f07f9a42e45b3b21133))
- **cli**: shinyhub schema command emitting clispec v0.2 document ([92f9593](https://github.com/rvben/shinyhub/commit/92f95930f7dd48bfd79fcc4240b871b272445cab))
- **cli**: clispec v0.2 schema generator walking the cobra tree ([d0f7611](https://github.com/rvben/shinyhub/commit/d0f761155494187c2cfe084878c39d49313dc4bb))
- **cli**: schema annotation registry for mutating/output_fields metadata ([5642c8f](https://github.com/rvben/shinyhub/commit/5642c8f14f978f3200cb2735d231c13a0a738f31))
- **cli**: global -o/--output and -q/--quiet with TTY auto-detection ([1dd8003](https://github.com/rvben/shinyhub/commit/1dd8003bedb8fe1f08d0e2b843cac81b3de15ee9))
- **cli**: unconditional structured error envelope as last stderr line ([9cb0fea](https://github.com/rvben/shinyhub/commit/9cb0feaffb0a149ea49af623c8303af4930d33bf))
- **cli**: central error classifier mapping errors to stable kinds ([6f3f95c](https://github.com/rvben/shinyhub/commit/6f3f95c1c62bec67937d4e52b7df6abe5bb5022f))
- **cli**: return typed httpStatusError from non-2xx responses ([7d33e48](https://github.com/rvben/shinyhub/commit/7d33e4887c92e9a53eb650b45f90a27241f7837a))
- **cli**: add structured error kind table ([d7cfab5](https://github.com/rvben/shinyhub/commit/d7cfab5c9561b03ab72e567d323668af8f132625))
- **deploy**: AWS ECS/Fargate Terraform module + iac-validate target ([c7871ac](https://github.com/rvben/shinyhub/commit/c7871ac58910532a9141ea623d6a1e67187013e3))
- **deploy**: docker-compose reference deployment ([5f2a0f7](https://github.com/rvben/shinyhub/commit/5f2a0f70c5ebd3f7e2343d320f611035fbe97140))
- **loadtest**: k6 harness - cold-start and websocket-session scenarios with make target ([2f17154](https://github.com/rvben/shinyhub/commit/2f171542198b7b7cc2b431b14676ee8a99eaae6d))
- **ui**: keep-warm replicas setting on the Configuration tab ([7ebcde2](https://github.com/rvben/shinyhub/commit/7ebcde2902de6ee0afc25524403285fef16cb7a8))
- **autoscale,api**: unified scale floor includes the warm floor; scale-up defragments warm pools ([ea26d9a](https://github.com/rvben/shinyhub/commit/ea26d9a994335b5ecd8267cdad254661eec97fd4))
- **proxy,lifecycle**: pool-degraded rejects trigger immediate warm expansion ([97af58a](https://github.com/rvben/shinyhub/commit/97af58a768c6f8bc4d254156f3fae3bc21900e52))
- **lifecycle**: re-expand warm-shrunk apps when traffic returns ([62d3636](https://github.com/rvben/shinyhub/commit/62d3636e447ddbc3872f5a5491fbe782a3abf961))
- **lifecycle**: warm-shrink replaces full hibernation when min_warm_replicas > 0 ([0cad0a0](https://github.com/rvben/shinyhub/commit/0cad0a06e9fe6904de55be84a46d20820f525c4f))
- **api**: WarmExpand boots warm victims back to full capacity ([63a5fbf](https://github.com/rvben/shinyhub/commit/63a5fbf012c0f4320da03492b0c98827aa6281e1))
- **api**: WarmShrink drains to the warm floor under the deploy lock ([bedf636](https://github.com/rvben/shinyhub/commit/bedf6368c37267407e5ac9ec7274994e4c3e13a3))
- **api,cli,deploy**: min_warm_replicas knob on PATCH, CLI, and manifest ([a3e5baf](https://github.com/rvben/shinyhub/commit/a3e5baff542b5d3b11deaff8ed712d463f1097d0))
- **db**: apps.min_warm_replicas + warm replica desired-state (migration 031) ([9d36517](https://github.com/rvben/shinyhub/commit/9d36517f8de0bc271ea2bb5a6a7a2b872163d048))
- **examples**: runnable streamlit, dash, and identity demos ([10627a2](https://github.com/rvben/shinyhub/commit/10627a29d8214932c09a37fa8848df77b5174747))
- **server**: wire identity env injection and proxy identity provider ([ce55be3](https://github.com/rvben/shinyhub/commit/ce55be30e0e1ae2ca6c90388df10c64c479281a5))
- **deploy**: manifest command override - validate-once template, per-replica substitution ([1ecdc3b](https://github.com/rvben/shinyhub/commit/1ecdc3b9d5c0fca0b8c54bf7e790683c34767f7e))
- **api,lifecycle,deploy**: wire effective identity flag through every pool-configuration path ([baa2335](https://github.com/rvben/shinyhub/commit/baa2335572ab67dd6f768f544470569308116d97))
- **proxy**: pool syncer propagates effective identity flag to non-owner instances ([4947837](https://github.com/rvben/shinyhub/commit/4947837016fdb208ad4c8dd1c78bd4bed6387a4f))
- **proxy**: identity header injection with per-pool atomic flag and injected provider ([5bd09b8](https://github.com/rvben/shinyhub/commit/5bd09b81bb558ce5384101d2f432ad03912e355f))
- **access**: propagate resolved user into request context (incl. public-app fast path) ([5268796](https://github.com/rvben/shinyhub/commit/5268796d8628e39b77b8f751e3424bf6edd57770))
- **deploy**: manifest [app] command template + identity_headers parsing and validation ([85a21e2](https://github.com/rvben/shinyhub/commit/85a21e204704d57f6407e351e4d0cf6e969ff456))
- **db**: apps.identity_headers column, manifest reconcile, routable-replica join (migration 030) ([4c2f48d](https://github.com/rvben/shinyhub/commit/4c2f48d8cd3642353d16a4348222448d3c62219a))
- **config**: global auth.identity_headers kill switch (default on) ([3450e26](https://github.com/rvben/shinyhub/commit/3450e26dcc171195e78324268563882b39f5d45d))
- **identity**: payload provider with TTL-cached single-flight groups resolution ([c6987af](https://github.com/rvben/shinyhub/commit/c6987af08d66ae5f032279610c3080f2974d6c6d))
- **identity**: per-app key derivation, group sanitization, token minting ([8cad791](https://github.com/rvben/shinyhub/commit/8cad79156e0d06090f0230cb396e3dd4b39b125f))

### Fixed

- **server-info**: report Python runtime by uv, not python3 ([1e44f0a](https://github.com/rvben/shinyhub/commit/1e44f0a7122367559c6eb4f86c39ca970a6d5426))
- **cli**: size apps list columns to content so long slugs stay aligned ([73ee58b](https://github.com/rvben/shinyhub/commit/73ee58bfdd41ec6844e406bfd1411d86a50852dc))
- **api**: replace inaccurate "never been deployed" with "no successful deployment" ([3c70e2c](https://github.com/rvben/shinyhub/commit/3c70e2c16d203a4fe8bf13f88bc8d4e45db52d58))
- **api**: surface actionable deploy failure reason instead of bare "deploy failed" ([c428936](https://github.com/rvben/shinyhub/commit/c428936187e26283c9b3e676a3690d79291a817a))
- **cli,api**: surface restart failures, verify liveness on start no-op, fix log tail framing ([395c66f](https://github.com/rvben/shinyhub/commit/395c66f789f4b270b8ddf6d9aca20b3effbe8381))
- **cli**: trigger env restart via the restart endpoint after a changed write ([48eb258](https://github.com/rvben/shinyhub/commit/48eb258829508b79f13ec359cc75210f676cc206))
- **cli**: resolve follow-mode format once and reject ndjson on document commands ([1f6c1fd](https://github.com/rvben/shinyhub/commit/1f6c1fded1c7e4f94a38ac91834bf4f7078341c7))
- **cli**: route progress and warnings to stderr, JSON deploy result on stdout ([d6bb7bc](https://github.com/rvben/shinyhub/commit/d6bb7bcfe0ae9220574e2631e6b89c8c2f1ab0ce))
- **cli**: mark flag-parse errors reported to avoid duplicate prose ([72108cd](https://github.com/rvben/shinyhub/commit/72108cd089a6cd32064c0bd0fb275eba3ed3aaf3))
- **cli**: validate pagination bounds and allow --fields on empty lists ([241701a](https://github.com/rvben/shinyhub/commit/241701a5cccef08e1f7b9c91dc8ceaeb0098b727))
- **cli**: preserve job exit codes and explicit format in error reporting ([bff65cc](https://github.com/rvben/shinyhub/commit/bff65cce55afa9ad66ac54f1868859a375575556))
- **cli**: classify cobra argument errors as validation and complete schema field declarations ([917dce3](https://github.com/rvben/shinyhub/commit/917dce3b8af9cc33e2cebde73ca0b57708c3dd78))
- **cli**: correct schema test fallthrough and annotation registry hygiene ([6fc1348](https://github.com/rvben/shinyhub/commit/6fc1348748aa8b2f9e563d52386d2f9fc9b0baa7))
- **cli**: strip nested brackets in schema positional names and declare output enum ([80ed5a5](https://github.com/rvben/shinyhub/commit/80ed5a53313aa8b0a62f3b029fe3ed3805f9279e))
- **cli**: classify deploy polling errors by HTTP status ([0ac1a0d](https://github.com/rvben/shinyhub/commit/0ac1a0dde1c46a03f50c6324b615959b51cc5ed0))
- **cli**: classify legacy transport exit code and pin classifier edge cases ([c3c761c](https://github.com/rvben/shinyhub/commit/c3c761c36f009755f8a9c5fcfcd09838a41fd7e6))
- **loadtest**: back off failed session iterations to prevent reconnect storms ([cc7e0c2](https://github.com/rvben/shinyhub/commit/cc7e0c2cbc51d6c0047c20af51783837bd19171d))
- **lifecycle**: symlink-normalize cwd comparison in process re-adoption ([b433da3](https://github.com/rvben/shinyhub/commit/b433da39817fdb0e039d0f6643f405edb8e0951d))
- **zdt**: startup pool adoption and unconditional waking-app reconcile close the handoff gap ([cf89799](https://github.com/rvben/shinyhub/commit/cf89799b50909593479e57608496b0c954aa57f0))
- **deploy**: path-parity data root for sibling-container app mounts ([ffb6687](https://github.com/rvben/shinyhub/commit/ffb6687a3e91c8c5dcb3ec9194c5c1d00da35635))
- **deploy**: chown data volumes for the distroless nonroot user ([07e7b4a](https://github.com/rvben/shinyhub/commit/07e7b4a4dc964e47f33ed6e068a574dec2782e2b))
- **loadtest**: cold-start measures page-ready and session-ready (ready probe needs a WS handshake) ([62679a6](https://github.com/rvben/shinyhub/commit/62679a6070e8db0276bcc8aa945a499f67fc54d9))
- **loadtest**: connect-latency metric scope and make-target param passthrough ([c55365b](https://github.com/rvben/shinyhub/commit/c55365bfddf1cabdefd3dd8094f6d60442a3e87f))
- **lifecycle**: reconcile and recovery treat warm victims as healthy stopped capacity ([b1cfed0](https://github.com/rvben/shinyhub/commit/b1cfed0b7db5bb4ad31bbffe7b78b6cfa3be3b36))
- **api**: WarmExpand registration, status guard, pool refresh ([e9cff4a](https://github.com/rvben/shinyhub/commit/e9cff4a29de9fa32826a1804365ab1b3d4fc1f51))
- **api**: clustered draining pre-write and hardened WarmShrink tests ([840f798](https://github.com/rvben/shinyhub/commit/840f7984a977db30f05bc9dff55dd479a1a67575))
- **api**: reconcile identity_headers even when the manifest [app] section is zero ([f43b161](https://github.com/rvben/shinyhub/commit/f43b161f9cf275070a034edc3721f2c8f0b47713))
- **proxy**: case-insensitive raw-key strip for inbound identity headers ([53353a2](https://github.com/rvben/shinyhub/commit/53353a2fda158977b708d0da6ebee0daab78f0e9))
- **identity**: panic-safe single-flight cleanup, concurrency test ([58a5973](https://github.com/rvben/shinyhub/commit/58a597334044fd01b184584e408b9bb083573153))

### Performance

- **lifecycle**: skip warm-shrink when already at the floor ([b16d42f](https://github.com/rvben/shinyhub/commit/b16d42ff076906dfdd1cc672b76f020ce4f24882))
- **lifecycle**: debounce burst-triggered warm expansion ([ed15b89](https://github.com/rvben/shinyhub/commit/ed15b896c860ddb075039180e0dcc613f898c191))

## [0.8.5](https://github.com/rvben/shinyhub/compare/v0.8.4...v0.8.5) - 2026-06-10

### Added

- **deploy**: launch Python apps under opentelemetry-instrument when auto-instrumentation is on ([c487bbf](https://github.com/rvben/shinyhub/commit/c487bbf14f7d4d55d897f2ea66191662af6620db))
- **process**: carry fleet auto-instrument default on the manager ([a56b311](https://github.com/rvben/shinyhub/commit/a56b311a4d9f2ed6e53227ac0a655d99044a800c))
- **deploy**: parse [tracing] auto manifest override ([f63e5d4](https://github.com/rvben/shinyhub/commit/f63e5d4303ec2254c29718b2f0727f806305bb80))
- **config**: add tracing.auto_instrument_apps option ([432cbab](https://github.com/rvben/shinyhub/commit/432cbab0e52751f65adcfcacffc2aeac4b48ddc6))

### Fixed

- **db**: qualify ambiguous source column in group-access reconcile upsert ([3b093bf](https://github.com/rvben/shinyhub/commit/3b093bf4c735e98e0822fe7e349279faa132fe6f))

## [0.8.4](https://github.com/rvben/shinyhub/compare/v0.8.3...v0.8.4) - 2026-06-10

### Added

- **oauth**: add oidc.require_valid_groups strict mode for malformed groups claim ([5ed4943](https://github.com/rvben/shinyhub/commit/5ed494385d13a92b85fc7314f6590b6d7a651591))
- **auth**: add forward_auth.require_groups_header strict mode ([67c2b3c](https://github.com/rvben/shinyhub/commit/67c2b3c14f2447949e7327a2d0c7a43275dbb5a9))

## [0.8.3](https://github.com/rvben/shinyhub/compare/v0.8.2...v0.8.3) - 2026-06-10

### Fixed

- **oauth**: treat malformed OIDC groups claim as unknown, not empty (no silent demotion) ([125ac5f](https://github.com/rvben/shinyhub/commit/125ac5f687a5a585387b84864406abc238ffdb53))
- **auth**: mint CSRF token for forward-auth users instead of rejecting mutations ([6b604a8](https://github.com/rvben/shinyhub/commit/6b604a847e17d99ab6f2b01f5c16311e37329815))
- **auth**: revoke group-derived roles when forward-auth groups header is absent ([8c79b67](https://github.com/rvben/shinyhub/commit/8c79b6799d2dcf77073bb4db444a2b232b282ab4))
- **db**: keep manifest reconcile from clobbering manual group rules ([a670206](https://github.com/rvben/shinyhub/commit/a6702062467edb4d90f6a1e3665dde90efb91d37))
- **db**: guard DeleteUser against deleting the last admin (concurrent-safe) ([feb26ca](https://github.com/rvben/shinyhub/commit/feb26ca758272e61e98ce1c18fdd3c013287a630))

## [0.8.2](https://github.com/rvben/shinyhub/compare/v0.8.1...v0.8.2) - 2026-06-09

### Added

- **ui**: mark manifest-sourced group rules read-only on the Access tab ([b59a866](https://github.com/rvben/shinyhub/commit/b59a866a7cd868219e60f4c61d832fd5202bbd6b))
- **api**: apply manifest [access] group rules on deploy (Phase C) ([5c3ecc4](https://github.com/rvben/shinyhub/commit/5c3ecc4a96e25e95b4efbdcfe60fab392837e374))
- **db**: reconcile manifest-sourced app group rules, preserving manual ([1dac1ac](https://github.com/rvben/shinyhub/commit/1dac1acee9f737d1a2f5c549e99b71edf5eb20b4))
- **deploy**: manifest [access] block (viewer_groups/manager_groups) ([c6de0b3](https://github.com/rvben/shinyhub/commit/c6de0b3f49b717aa33990aa51a14b53991324a33))
- **api**: expose can_manage so per-app member/group managers get the management UI ([6aa42bd](https://github.com/rvben/shinyhub/commit/6aa42bd1df70fbfd25e4fc880a433f5bfc0df3fb))
- **cli**: apps access group-grant/group-revoke/group-list ([171b435](https://github.com/rvben/shinyhub/commit/171b4357b02692d8b8e5899626106c2e7f1659d2))
- **ui**: Access-tab group-access rules (list/add/remove) with additive-only hint ([87e51e4](https://github.com/rvben/shinyhub/commit/87e51e4dfcd8cc52a108577a3f32b8977b90e859))
- **api**: per-app group-access CRUD endpoints with additive-only advisory ([f0d705d](https://github.com/rvben/shinyhub/commit/f0d705d4792c7414526dc98b3e898245c0ca421f))
- **api**: honor per-app group role in manage and explicit-access checks ([2f72f46](https://github.com/rvben/shinyhub/commit/2f72f46c89fa87e70a43f2a59f91978b16b27183))
- **db**: app_group_access CRUD + group-aware UserCanAccessApp and app list ([b4933f9](https://github.com/rvben/shinyhub/commit/b4933f9425872d11deef337c6b21ea3b8e0ada8e))
- **db**: migration 029 - app_group_access per-app group rules table ([4ee7db8](https://github.com/rvben/shinyhub/commit/4ee7db8ae301179f872faa6b4e5c23004d7a1482))
- **cli**: thread, report, and optionally wait for first-fire in fleet apply ([fabb887](https://github.com/rvben/shinyhub/commit/fabb887612606776b026f123459ce6f03a4311a2))
- **ui**: (SSO-managed) option to clear a user's manual role override ([a0af8b5](https://github.com/rvben/shinyhub/commit/a0af8b5741151d0eda8c42748da90d1bf99f9106))
- **cli**: report and optionally wait for run_on_register first-fire on deploy ([1387d7f](https://github.com/rvben/shinyhub/commit/1387d7fae8771b101551b10762a070e50ef89c5c))
- **api**: user role PATCH sets/clears manual override (empty role = SSO-managed) ([1868eec](https://github.com/rvben/shinyhub/commit/1868eec69f9f9cecca01cb68e76d7d56c48c85b7))
- **cli**: add first-fire ref parsing and run-poll helpers ([06c135d](https://github.com/rvben/shinyhub/commit/06c135d6a3e7b967a41363fe04803f24b0b68496))
- **cli**: add --run-on-register and --follow to schedule add ([0014c13](https://github.com/rvben/shinyhub/commit/0014c1324092eb5eb890a2da9153cf38f6b22ade))
- **auth**: forward-auth full role reconcile covering /app/* via top-level mux ([89eb352](https://github.com/rvben/shinyhub/commit/89eb352d6dc772299d613cdaa0e1734345e7040f))
- **schedules**: accept run_on_register on schedule create and return the run id ([7c57b18](https://github.com/rvben/shinyhub/commit/7c57b18a7ac572cc69f42b3bdb74e56473fae0b4))
- **api**: reconcile global role from OIDC groups on login ([05f6bd6](https://github.com/rvben/shinyhub/commit/05f6bd6839b640228634ca217485ecbb5831548e))
- **schedules**: fire run_on_register once on first registration during deploy ([6593aad](https://github.com/rvben/shinyhub/commit/6593aadd472591e84be96b0ae03b74f99b44fca3))
- **oauth**: extract configurable OIDC groups claim and optional scope ([e0fc223](https://github.com/rvben/shinyhub/commit/e0fc2239ac7ed6d9342bbb097612ca2be4ac6bd6))
- **db**: authoritative group reconcile, manual role override, last-admin guard ([dd94e0d](https://github.com/rvben/shinyhub/commit/dd94e0d2e2f0b725c45538f471f30149c8491f75))
- **manifest**: add run_on_register field to [[schedule]] ([2095dda](https://github.com/rvben/shinyhub/commit/2095ddac00ea9c7e1b770549a12591c1fbaf73b8))
- **config**: group_role_mappings, OIDC groups claim/scope, admin_groups alias merge ([f67564e](https://github.com/rvben/shinyhub/commit/f67564e2b1cd46dba1691720f23b50870932dd4a))
- **auth**: ResolveGlobalRole group-to-role mapping engine ([9a7cc65](https://github.com/rvben/shinyhub/commit/9a7cc656ded43412372bd09a39c668e0da2abb56))
- **db**: migration 028 - user_groups + role provenance columns with viewer-baseline seed ([af35290](https://github.com/rvben/shinyhub/commit/af35290a893897796ece157bb27f2231360c7baa))
- **api**: reject self-revoke and dedupe member-grant existence check ([18442e1](https://github.com/rvben/shinyhub/commit/18442e1b7b153866c622a6af7951a64084fe8d21))
- **api**: reject self member-role change to prevent manager self-lockout ([2d78a3f](https://github.com/rvben/shinyhub/commit/2d78a3f3791b7c03cc33b6d9db3bd55e0e1ef402))
- **cli**: apps access grant/revoke/list with --role ([e12724d](https://github.com/rvben/shinyhub/commit/e12724d6e48614fb371fe48dbad662d813694eff))
- **ui**: editable member role dropdown on the app Access tab ([04b479f](https://github.com/rvben/shinyhub/commit/04b479fe8a91fb5b3d1581b15f50e3645b7de7ae))
- **api**: assign app member roles via PATCH members and role on grant ([2044a09](https://github.com/rvben/shinyhub/commit/2044a09546cd86d40498a90267786f9ba6de5e3c))
- **db**: role-aware app-member grant and not-found-aware SetMemberRole ([466ba80](https://github.com/rvben/shinyhub/commit/466ba805c25d9de58502620bfb8f37140e17fa70))

### Fixed

- **api**: reject API revoke of manifest group rules; label group-access audit actions ([ac1b825](https://github.com/rvben/shinyhub/commit/ac1b825e940e93bbe4d1503da90c4aac5c4aaf88))
- **schedules**: return 201 not 500 when the scheduler is not yet started ([7a83c37](https://github.com/rvben/shinyhub/commit/7a83c37d8c27a78b1960f3fb8891374607c5e6d7))
- **api**: propagate DB errors in per-app group authz instead of degrading to 404 ([c93109d](https://github.com/rvben/shinyhub/commit/c93109d1a603485f3fd429fc0eb414ca243e4c67))
- **cli**: treat first-fire wait timeout as non-fatal on single deploy (match fleet apply) ([ab29c2f](https://github.com/rvben/shinyhub/commit/ab29c2f8afb4817504a551b49734503a44ab4109))
- **api**: make member grant additive so POST cannot self-downgrade or silently demote ([e7eca78](https://github.com/rvben/shinyhub/commit/e7eca7873687b60940f7de371d017f06a3a63eee))

## [0.8.1](https://github.com/rvben/shinyhub/compare/v0.8.0...v0.8.1) - 2026-06-09

### Added

- **proxy**: DB-driven pool sync so every instance can serve off-host apps ([1bce4fd](https://github.com/rvben/shinyhub/commit/1bce4fd13918fd08d8c2e787db4983041cd2380b))
- **api**: /readyz (serving) + /activez (active) readiness contract ([3350790](https://github.com/rvben/shinyhub/commit/3350790c770a2e0e84305e47d6debc23ba46fad2))
- **worker**: enforce + persist the per-replica session hard cap (incl. re-adoption) ([7a15820](https://github.com/rvben/shinyhub/commit/7a158203be93cfcc2ecf44159d67ac0fcd3d28ee))
- **proxy**: deployment-stamp the sticky cookie so redeploys re-pin cleanly ([506bc28](https://github.com/rvben/shinyhub/commit/506bc2827000698e8ab3a37141a449c2fd46e127))
- **proxy**: build worker mTLS transport from the DB replica row, not the registry ([4b819f2](https://github.com/rvben/shinyhub/commit/4b819f2798f76dc8e32a35e24e611817a3741c4f))
- distributed scale-down drain (local CAS + DB desired_state + fleet wait) ([eec3e45](https://github.com/rvben/shinyhub/commit/eec3e458f2c68894950f6bfca5c601bb69d6dc1a))
- **lifecycle**: cross-instance wake reconciler + forward-error wake recovery ([f533f49](https://github.com/rvben/shinyhub/commit/f533f497d29e1d57109ad3d0f941315ce6265195))
- **lifecycle**: conservative fleet-idle hibernation CAS in clustered mode; single-node unchanged ([be5728d](https://github.com/rvben/shinyhub/commit/be5728d85f11ae2cedec02a4e0d20d9856a9a2cc))
- **autoscale**: scale on the fleet-wide session count in clustered mode ([bbf56fb](https://github.com/rvben/shinyhub/commit/bbf56fb62ebef89de4f34c878277113ac8db91f7))
- **proxy**: report local replica session counts + activity to the DB (clustered only) ([ac475b5](https://github.com/rvben/shinyhub/commit/ac475b5939f3bd91f53be27f065a58a2f1a45463))
- **db**: replica_sessions table + AppFleetLoad + stale reaper (both dialects) ([e74629c](https://github.com/rvben/shinyhub/commit/e74629ccc466b7bfeeb931bcb76134db88a9bd42))
- **cmd**: refuse native/local-docker runtimes in a clustered deployment ([daa8e71](https://github.com/rvben/shinyhub/commit/daa8e71c91260daef380c06ee3f3a75aeb6b92db))

### Fixed

- **db**: base replica-session staleness on the DB clock to tolerate control-plane clock skew ([4e65037](https://github.com/rvben/shinyhub/commit/4e65037f1132e5e6f7bf977ada742ac7b5bf0831))

## [0.8.0](https://github.com/rvben/shinyhub/compare/v0.7.5...v0.8.0) - 2026-06-08

### Added

- refresh worker registry on lease acquire; gate mutations on owner-and-ready ([99f27ff](https://github.com/rvben/shinyhub/commit/99f27ffc5d625ee351865e89d723cf7614fc9ead))
- **worker**: retry registration while the control plane is not the ready owner (503) ([fd9baea](https://github.com/rvben/shinyhub/commit/fd9baea4e1a3c3f97aed2ca063dc6116fdc8fcb7))
- **api**: owner-gate worker register/heartbeat; DB-authoritative auth for bundle-fetch ([a363c39](https://github.com/rvben/shinyhub/commit/a363c392486eca22e77263a6c570bfeb569a9c2d))
- **worker**: Registry.Refresh rebuilds the routing index from the DB on takeover ([7bc8f3b](https://github.com/rvben/shinyhub/commit/7bc8f3b6ae199277b46655bf800955791489b692))
- **autoscale**: persist cooldown to apps.last_autoscale_at, arm on first step ([3b6196d](https://github.com/rvben/shinyhub/commit/3b6196dcf4565fce1d092b2c5152630c19f46ae3))
- **db**: persist apps.last_autoscale_at (App field, scan, setter) ([ba2790c](https://github.com/rvben/shinyhub/commit/ba2790c5791ec52c11b49538828070b45f44839c))
- **db**: add apps.last_autoscale_at migration (both dialects) ([b402fac](https://github.com/rvben/shinyhub/commit/b402fac691b9a925903a0dc4e3386c941d44587e))

### Fixed

- **leader**: derive local lease deadline from pre-call time (conservative vs DB) ([c19511e](https://github.com/rvben/shinyhub/commit/c19511ef64ca9b57fdb4fbdaa734f94dbf6ae407))
- **autoscale**: enforce cooldown at whole-second resolution, never disabled ([4967166](https://github.com/rvben/shinyhub/commit/49671668d809257d95bb30f884bb6e959adbc505))
- **worker**: retry registration on connection reset, not just refused ([9d5523a](https://github.com/rvben/shinyhub/commit/9d5523acc482db0bedc702beaaeb3ddcb02e725d))
- **autoscale**: measure cooldown against the DB clock; compare as duration ([e583d0f](https://github.com/rvben/shinyhub/commit/e583d0f2bf709b3e432530dd1831d546f2ae1ccd))
- **leader**: relinquish ownership when renew fails past the local lease deadline ([186021c](https://github.com/rvben/shinyhub/commit/186021c6a8c079dd3de6629541714da8660e1113))
- **worker**: serialize MarkDown/Forget with regMu against Refresh ([982af1a](https://github.com/rvben/shinyhub/commit/982af1a4b0fd32efb9d1b3b4953772400c5a6fcd))

## [0.7.5](https://github.com/rvben/shinyhub/compare/v0.7.4...v0.7.5) - 2026-06-05

### Fixed

- **worker**: mirror the public CA cert to disk for worker bootstrap ([6e980ea](https://github.com/rvben/shinyhub/commit/6e980eaea00a4c743404f0b3bec7b2288120ada1))

## [0.7.4](https://github.com/rvben/shinyhub/compare/v0.7.3...v0.7.4) - 2026-06-05

### Added

- backfill relative deployment bundle_dir to absolute on boot ([7ccbf97](https://github.com/rvben/shinyhub/commit/7ccbf97201fcd06cb4382ca0f62c4b4a6f513c4b))
- **config**: normalize storage roots to absolute paths on load ([ef5ff7f](https://github.com/rvben/shinyhub/commit/ef5ff7fa37b8db90e4cc1f5cbdd0617ccd0d688c))
- **worker**: source the worker CA from the DB at boot via LoadOrInitCA ([026c928](https://github.com/rvben/shinyhub/commit/026c92892f516607ce8b8b2e68fa26630f15a13e))
- **worker**: LoadOrInitCA with DB-backed CA, import, race-safe init, mismatch guard ([f5a69f2](https://github.com/rvben/shinyhub/commit/f5a69f2a457480effe138d7c71240c49566a8a2b))
- **worker**: harden loadCA validation and extract generateCA ([2f7443a](https://github.com/rvben/shinyhub/commit/2f7443adb1f2e76cb40a4fb4f66fed1284b62af8))
- **db**: cp_worker_ca store accessors (GetWorkerCA/PutWorkerCAIfAbsent) ([dbae767](https://github.com/rvben/shinyhub/commit/dbae76723e312f1629ecbee86df1dbf6a17b26de))
- **db**: add cp_worker_ca migration (both dialects) ([18193aa](https://github.com/rvben/shinyhub/commit/18193aaf18884a3e4d83a2344deb2c1516d98fde))
- **secrets**: add DeriveKeyWithInfo for HKDF domain separation ([d28a76e](https://github.com/rvben/shinyhub/commit/d28a76eae2f714408699a373b28b61afbbea0d3a))
- **db**: per-dialect migration dirs with consolidated Postgres baseline ([1ba5361](https://github.com/rvben/shinyhub/commit/1ba5361bca1cb7e268a0253dffc59e8349dbf0a4))
- **db**: dispatch Open by DSN scheme to SQLite or Postgres ([a96bcb6](https://github.com/rvben/shinyhub/commit/a96bcb65694ccaa47a88d69455a8baaa014a2de4))
- **db**: add quote-aware ?->$n rebind for Postgres dialect ([6dd8bdc](https://github.com/rvben/shinyhub/commit/6dd8bdc1f2bb2905e11a39197e161d71f8521a83))
- **ui**: style the transient waking app-status badge ([f6f13c1](https://github.com/rvben/shinyhub/commit/f6f13c1dab8c6d101a8a01015b039138c962e76f))
- **lifecycle**: wake hibernated apps via DB CAS instead of an in-memory guard ([99fbc87](https://github.com/rvben/shinyhub/commit/99fbc87567e20fec05ab5be8b61e55f9408c5803))
- **db**: add BeginWake/AbortWake CAS for cross-process hibernation wake ([08cad47](https://github.com/rvben/shinyhub/commit/08cad4747500b59441637073d67f5e401075838f))
- **server**: hand off listeners across SIGHUP via tableflip for zero-downtime upgrades ([8965a40](https://github.com/rvben/shinyhub/commit/8965a40e4aa8b646cc8deb3fffde27448e886e22))
- **upgrade**: wire SIGHUP->Upgrade, ctx->Stop, and sd_notify MAINPID retarget ([05c025f](https://github.com/rvben/shinyhub/commit/05c025ff0222e7a6fa19ef11575cfc661156cc0a))
- **upgrade**: add tableflip-backed Upgrader interface for zero-downtime restart ([2feeb21](https://github.com/rvben/shinyhub/commit/2feeb2126cdfa2bb4e02cf996c7064f8bf49cea4))
- **config**: add server.upgrade_timeout and server.pid_file for zero-downtime upgrades ([d0f2a94](https://github.com/rvben/shinyhub/commit/d0f2a94e04094330bd44bdd49a01751a7ee938bb))
- **server**: drain WebSocket sessions on shutdown and report unready while draining ([dd80cc3](https://github.com/rvben/shinyhub/commit/dd80cc3a289a0e0f2a2ce602a680f568f7679d09))
- **config**: add server.drain_timeout for graceful connection draining ([83fc359](https://github.com/rvben/shinyhub/commit/83fc359acb827d2935c8458cdaa0568af54139ad))
- **proxy**: register hijacked WebSocket connections for graceful drain ([3d1d628](https://github.com/rvben/shinyhub/commit/3d1d628beae64ed61f21a50084aefe3abc731916))
- **proxy**: track hijacked connections and add instance drain primitives ([e17ed78](https://github.com/rvben/shinyhub/commit/e17ed78c1e1084eabaed3baec8d44f7c0f294547))
- **server**: gate background loops and boot reconciles behind control-plane ownership ([2032325](https://github.com/rvben/shinyhub/commit/2032325e25da3be19439f452c916dda9fa1e7a64))
- **api**: gate mutating requests behind control-plane ownership (503 on non-owner) ([a630eea](https://github.com/rvben/shinyhub/commit/a630eeafcf21b01586ec0f3a1d0d49705db7de14))
- **config**: add control-plane instance_id and ownership-lease tunables ([7b3dc52](https://github.com/rvben/shinyhub/commit/7b3dc528392804e3e325693ceab9a8d590a47f24))
- **leader**: add OwnerScope per-ownership-span context lifecycle ([1a1d1ee](https://github.com/rvben/shinyhub/commit/1a1d1eed21f4d396cbdaeb838759e9a7b86c42ad))
- **leader**: add Elector.Epoch and clamp lease TTL to 2x renew interval ([6f030e7](https://github.com/rvben/shinyhub/commit/6f030e7e3cf49655f93362e207d19d5b1325aa49))
- **leader**: add fenced-lease Elector (acquire/renew loop, ownership callbacks) ([1cb2889](https://github.com/rvben/shinyhub/commit/1cb2889489b70a174eae551502bd5db0087c1092))
- **db**: add fenced cp_owner lease (acquire/renew/release/get) ([d5f0ca0](https://github.com/rvben/shinyhub/commit/d5f0ca0e2916280b9e0a4a5cb2b6017888578313))
- **api**: wire forward-auth middleware ahead of bearer on protected routes ([c58d08e](https://github.com/rvben/shinyhub/commit/c58d08ef252fbf1e12c12eb1fd3c0b42f617512b))
- **db**: add forward-auth user store adapter methods ([23df72f](https://github.com/rvben/shinyhub/commit/23df72f053bf8f1ee350b66e7d2a741e0e76664b))
- **auth**: BearerMiddleware yields to a pre-authenticated context user ([a67ef40](https://github.com/rvben/shinyhub/commit/a67ef40a84fd52e8d60e73ae4b7f979dd6e3b78f))
- **auth**: add forward-auth middleware for trusted reverse proxies ([528fe9b](https://github.com/rvben/shinyhub/commit/528fe9b0794a7c30868ac9a26afb267475f7ae15))
- **config**: add forward_auth config block with env overrides ([2633de6](https://github.com/rvben/shinyhub/commit/2633de6fcdad628726c250b136a29ca66f5e69cd))
- **skills**: add deploy-shinyhub agent skill with CI smoke + lint guard ([855c716](https://github.com/rvben/shinyhub/commit/855c716215e2b2cac5282bade8a8f936dd68e28d))

### Fixed

- **worker**: validate disk CA before persisting; reject partial disk CA state ([91486d4](https://github.com/rvben/shinyhub/commit/91486d4f9e009b81d653e0a2cb10c30da39c1fe2))
- **db**: Postgres compatibility for LIMIT, EXISTS, unique checks, and tx abort ([37ad342](https://github.com/rvben/shinyhub/commit/37ad34239eee63f0ce0a287e4e50c2f3f92f5377))
- **db**: use dialect-aware unique-violation check in CreateSchedule ([8fca0e7](https://github.com/rvben/shinyhub/commit/8fca0e736b79ecde336f1ecd2b5b78ee8fdf3486))
- **db**: type cp_owner timestamps as timestamptz on Postgres ([ee0dd60](https://github.com/rvben/shinyhub/commit/ee0dd606f0a3a72f6747e9f0afa53c6f6fbb80d6))
- **db**: serialize shared-data grants via dialect beginWrite + advisory lock ([71471f2](https://github.com/rvben/shinyhub/commit/71471f26f2c519b50d765d2b994fe4aa73d8d852))
- **db**: scan integer boolean columns via intermediate int for Postgres compatibility ([854bfab](https://github.com/rvben/shinyhub/commit/854bfabf2ba102e9e6c4b0bfc582383378a352f2))
- **lifecycle**: tear down wake-started replicas when a concurrent delete removes the app ([1a5116c](https://github.com/rvben/shinyhub/commit/1a5116c607c83e36ec69e9ed7060b01273c0b5e1))
- **lifecycle**: tear down replicas when a wake is superseded by stop/delete ([886fb1d](https://github.com/rvben/shinyhub/commit/886fb1d0ccfb56817272eba8ec22448e89001a3f))
- **lifecycle**: make hibernation wake robust against failure and concurrent intent ([e4921af](https://github.com/rvben/shinyhub/commit/e4921af261036d108ea3b1dd9c2ebf3e19384b55))
- **server**: clean up startup-error paths, exit SIGHUP goroutine on ctx, tighten handoff e2e ([854f378](https://github.com/rvben/shinyhub/commit/854f3783c699a80652757b09682e7f5b54aa6b8b))
- **server**: keep reconcile-before-recover order under ownership and make watcher restart-safe ([8ad6785](https://github.com/rvben/shinyhub/commit/8ad6785b7d89d0ecb599e744a342f897f882a404))
- **skills**: make deploy-shinyhub frontmatter valid YAML and harden skill-lint ([e2de7db](https://github.com/rvben/shinyhub/commit/e2de7db607b18393bfadb714cb88259d72ea2e8b))

## [0.7.3](https://github.com/rvben/shinyhub/compare/v0.7.2...v0.7.3) - 2026-06-04

### Fixed

- **worker**: run app container as the bundle-owner uid with a writable HOME ([e9c2820](https://github.com/rvben/shinyhub/commit/e9c28209f76665963f313eb0f7c91d41bbbfce5d))
- **worker**: pull the app image before creating a container ([42db42f](https://github.com/rvben/shinyhub/commit/42db42f4408ec9446c8138435d0c2b59a0860438))
- **worker**: make a worker routable only once its data-plane listener is up ([bd3029f](https://github.com/rvben/shinyhub/commit/bd3029fc033b717ae1905e3641b7893a4dce8ea1))

## [0.7.2](https://github.com/rvben/shinyhub/compare/v0.7.1...v0.7.2) - 2026-06-03

### Added

- **fargate**: secret lifecycle, fail-closed, and churn avoidance ([9d4e463](https://github.com/rvben/shinyhub/commit/9d4e46370ad437bdf19ab884846c154938bce3e4))
- **fargate**: route secret env vars through AWS Secrets Manager ([39fc9fc](https://github.com/rvben/shinyhub/commit/39fc9fcd1d3353dc52d2126121fefa650aab64da))
- **env**: carry secret env vars separately from plaintext in the start path ([463fe16](https://github.com/rvben/shinyhub/commit/463fe168103b4d7bbcb7be93cab0f64e1fe6938c))
- **security**: rate-limit OAuth/OIDC callback endpoints ([e0e4414](https://github.com/rvben/shinyhub/commit/e0e44149e9d8591e2a06084201c3be1be69b8b1c))
- **security**: strengthen auth defaults (bcrypt cost 12, JWT not-before) ([60ebed0](https://github.com/rvben/shinyhub/commit/60ebed009761ff7fe2fc51a4c7eb1c1c64198709))

### Fixed

- **ui**: grant app access by username, not via the operator-only user lookup ([d14140a](https://github.com/rvben/shinyhub/commit/d14140a26b6015ef4b6a3436046aeb2acefcd537))
- **api**: make app env config manager-only; tighten schedule reads ([a61c96d](https://github.com/rvben/shinyhub/commit/a61c96d4d16401b7158a70f28dfde21b2f42aa44))
- **proxy**: sign the sticky-routing cookie; guard CSRF/auth invariant ([3dc66af](https://github.com/rvben/shinyhub/commit/3dc66af754a08e1f36e870fe3f623948065ce2fb))
- **api**: stop arbitrary user enumeration; grant access by username server-side ([29dbe8a](https://github.com/rvben/shinyhub/commit/29dbe8a88d79e61738ea24a13fd22f4e53e4af9f))
- **proxy**: stop leaking session cookies and trusting spoofed forwarding headers ([6a41a25](https://github.com/rvben/shinyhub/commit/6a41a255758cc75ac4a8e52854544dd1a6760097))

## [0.7.1](https://github.com/rvben/shinyhub/compare/v0.7.0...v0.7.1) - 2026-06-02

### Added

- **security**: restrict file and directory permissions ([8030d9f](https://github.com/rvben/shinyhub/commit/8030d9f4c52130568b11a2f03702dd35851ee11b))
- **security**: record old/new values in role-change and access-change audit events ([aa577b1](https://github.com/rvben/shinyhub/commit/aa577b1504d8dd15d94ace0f87b6f4b254325662))
- **security**: harden bundle ZIP extraction ([0e2f02e](https://github.com/rvben/shinyhub/commit/0e2f02ee27d02d3a64de8dade1bcb4b1f685a39f))
- **security**: harden Docker app containers ([7eeebf8](https://github.com/rvben/shinyhub/commit/7eeebf83d1ef892b243c8cffd0b7342580376803))
- **security**: guard account self-modification and enforce create-user password length ([403cee2](https://github.com/rvben/shinyhub/commit/403cee2809bb14612ae50ff176878c4137e93268))
- **security**: add HTTP security headers to control-plane responses ([49302a7](https://github.com/rvben/shinyhub/commit/49302a77608d9b61b36517cd20416f3e12c92da1))

### Fixed

- **security**: keep Docker /app bundle mount writable for in-container dep prep ([4a5c783](https://github.com/rvben/shinyhub/commit/4a5c78347de01029971afb86ef38a633453d4cca))

## [0.7.0](https://github.com/rvben/shinyhub/compare/v0.6.2...v0.7.0) - 2026-06-02

### Added

- **ui**: responsive layout and resilient list/stream states ([a3a0206](https://github.com/rvben/shinyhub/commit/a3a02069e46719f30a16893919a89f25880a8b4e))
- **ui**: keyboard accessibility for modals and controls ([f095ca1](https://github.com/rvben/shinyhub/commit/f095ca1fc77de188d473b789e740e378e5db748e))
- **api,ui**: add fleet-health overview (/api/fleet/health + admin grid banner) ([a9514d7](https://github.com/rvben/shinyhub/commit/a9514d7f75114e95fa0dbacf39d104edf48a7b92))
- **ui**: add admin Workers page (read-only fleet view) ([0282d1e](https://github.com/rvben/shinyhub/commit/0282d1e9070d23b78f322ec7a856a971c7aed19d))
- **api,ui**: surface lost-replica reason in live metrics and detail panel ([2fc4213](https://github.com/rvben/shinyhub/commit/2fc42130d58a95cc4a84e79ed6fb6d6b3b49b981))
- **config**: add SHINYHUB_RUNTIME_AUTOSCALE_* env overrides ([eebde4f](https://github.com/rvben/shinyhub/commit/eebde4fc86b0406b57f12af77f6c1022d7cda24d))
- **api**: reject R apps deployed to Fargate tiers ([8e7792b](https://github.com/rvben/shinyhub/commit/8e7792b9720fd968953ebb3181e6ae63336aec95))
- **cli,api**: add server-readiness preflight with --wait-for-server ([f68efbc](https://github.com/rvben/shinyhub/commit/f68efbcad6535bd5803391f8f3c9b895af64483a))
- **cli**: add offline `fleet validate` subcommand ([d9499b8](https://github.com/rvben/shinyhub/commit/d9499b8e94fe56c2bd17a92aaecb342229e2fc27))
- **main**: wire per-tier LaunchType into buildFargateRuntime for ECS EC2 support ([c166b29](https://github.com/rvben/shinyhub/commit/c166b29eefb91dd9121911f2d934544be0d4d612))
- **config**: add per-tier launch_type for ECS EC2 support with FARGATE/EC2 validation branching ([8c1b41a](https://github.com/rvben/shinyhub/commit/8c1b41a2bb63e500db6571f377e27da13f5e9373))
- **lifecycle**: add WorkerID() to FargateTaskSweeper interface and use it in sweep handle construction ([f31c148](https://github.com/rvben/shinyhub/commit/f31c148f080a2504f1edc1a883e569e67ed9026d))
- **fargate**: branch runTaskInput on launch type - EC2 sets LaunchTypeEc2 and omits PlatformVersion ([2fde0a1](https://github.com/rvben/shinyhub/commit/2fde0a1ac5d76e80cc6bdb48c521e8f3ed5272b3))
- **fargate**: filter Inventory and ListManagedTasks by launch type via client-side DescribeTasks ([cad476a](https://github.com/rvben/shinyhub/commit/cad476a7680829b04ee58ee9813530c49e7b8f9d))
- **fargate**: add EC2WorkerID, per-runtime workerID field, IsECSManagedWorkerID, and method-form handle encode/decode ([4fdacb9](https://github.com/rvben/shinyhub/commit/4fdacb911fdb7561769b76cd2da30c77613ae042))
- **fargate**: add workerID segment to clientToken to prevent EC2/Fargate cross-contamination ([525293e](https://github.com/rvben/shinyhub/commit/525293ea4e58573a0bf1ec5379dc9c6dae33c4f6))
- **ui**: add CSS for autoscale badge classes, kill-switch warning, and grid autoscale badge ([3dfe28f](https://github.com/rvben/shinyhub/commit/3dfe28f9e877a8b4c89ae531fe707d73328c6e4a))
- **ui**: wire tier/provider/metrics_available in seedReplicasFromStatus and poll-driven autoscale_status refresh ([aba6f56](https://github.com/rvben/shinyhub/commit/aba6f5624fe3808efcb2651240261fdbaae0bffd))
- **ui**: wire replica-display, metrics_available, autoscale badge, and deduplicated knownActions in app.js ([ef0b67c](https://github.com/rvben/shinyhub/commit/ef0b67c1ee17aa524a2f9b13caba1accacf59263))
- **ui**: add formatRelative/formatCountdown and last-scaled/cooldown/kill-switch to autoscale summary ([3f0c2f0](https://github.com/rvben/shinyhub/commit/3f0c2f06c45043ba7390f79cd8a865ec5f198a99))
- **ui**: add replica-display.js helper for backend labels and honest PID-less metrics ([c1fa323](https://github.com/rvben/shinyhub/commit/c1fa3236c8a3cbb60bf07044945a9691ac02111b))
- **api**: add autoscale_status, global_autoscale_enabled, and per-replica tier/provider/metrics_available to metrics poll and app envelope ([0122d18](https://github.com/rvben/shinyhub/commit/0122d185f74a4aa4e2d39f3e2dff4b86fba45774))
- **db**: add LatestAutoscaleEvent query for DB-backed autoscale live status ([f98f31d](https://github.com/rvben/shinyhub/commit/f98f31d25bf0d0ca0a72cd723f28fc5bd20d7475))
- **metrics**: add shinyhub_autoscale_scale_total counter; wire to autoscale controller ([a7f6725](https://github.com/rvben/shinyhub/commit/a7f6725f4eab759256beae0054408d41df88f9d8))
- **autoscale**: inject AuditRecorder; record audit event + reason on each scale action ([ee20c65](https://github.com/rvben/shinyhub/commit/ee20c659fc32221db6f228ad7771c4e2f624b327))
- **autoscale**: decision returns (desired, reason) for audit and metric context ([97c0ae9](https://github.com/rvben/shinyhub/commit/97c0ae92c49b43a9a2dd4bd259ee738620df0359))
- **runner**: add reference Python Fargate runner image with fetch-verify-prep-exec entrypoint ([8b14fcb](https://github.com/rvben/shinyhub/commit/8b14fcbc97ace2ae0e53ba081faa856c97f4770a))
- **fargate**: inject SHINYHUB_CONTROL_PLANE_URL and SHINYHUB_BUNDLE_TOKEN into task env ([ed6c3e9](https://github.com/rvben/shinyhub/commit/ed6c3e97045c042c97c5127d0fb9b273f9fbc90f))
- **serve**: mount GET /internal/fargate-bundle/ on main mux with HKDF key derivation ([72caeac](https://github.com/rvben/shinyhub/commit/72caeac242df4691297ab5dedc9ae0cdbee69ced))
- **api**: extract serveBundleByDigest helper and add FargateBundleHandler with bearer auth ([c2eaa51](https://github.com/rvben/shinyhub/commit/c2eaa51c04031ff6bd830f97d2eb93b2a6f58f16))
- **config**: add Fargate control_plane_url + bundle_token_ttl config fields with https gate ([53389ee](https://github.com/rvben/shinyhub/commit/53389ee6f9dd1ae8f109292f75f85847f47a02ea))
- **bundletoken**: add Mint/Verify HMAC capability token package ([c16a0bd](https://github.com/rvben/shinyhub/commit/c16a0bd741b92bdbb387ee0c04b54139edaf0f81))
- **api**: reject memory/cpu over Fargate task ceiling at PATCH /api/apps/:slug for single-tier Fargate ([846f30c](https://github.com/rvben/shinyhub/commit/846f30c7ab4a24867259cd2e61c4e62816a6b7db))
- **fargate**: wire TaskCPUUnits and TaskMemoryMB from config into fargate.Config at startup ([076a231](https://github.com/rvben/shinyhub/commit/076a231517bab4701375936af3261045a1c0fa1c))
- **fargate**: add TaskCPUUnits/TaskMemoryMB to Config; clamp containerOverride to task ceiling with Warn ([92ee5bf](https://github.com/rvben/shinyhub/commit/92ee5bfe2237a9c73c7858cffa25f07472d20b41))
- **config**: add DefaultResourcesForTier accessor dispatching on tier runtime ([946b325](https://github.com/rvben/shinyhub/commit/946b325c0c6a469785bd9c8a3faba485a2dcea09))
- **config**: validateFargate enforces full Fargate CPU/memory matrix with increment rules ([26e3633](https://github.com/rvben/shinyhub/commit/26e36335e00b8e27f6b82e9f119640bc33d6d13f))
- **config**: add applyEnv entries for Fargate resource fields with error-returning parse ([e6ccb15](https://github.com/rvben/shinyhub/commit/e6ccb15684d8c749a8258340a0d88c901b92e7d2))
- **config**: add task_cpu_units, task_memory_mb, default_memory_mb, default_cpu_percent to FargateRuntimeConfig ([9d8b5de](https://github.com/rvben/shinyhub/commit/9d8b5debe2941c5236a511a491493ac8ff78b4cd))
- **serve**: wire EnableLostReplicaHealing and orphan sweep for fargate tiers ([a6bde4e](https://github.com/rvben/shinyhub/commit/a6bde4e2d8a9094cff545b6e8e93268fd366852d))
- **lifecycle**: add FargateTaskSweeper interface and SweepOrphanFargateTasks ([04a28de](https://github.com/rvben/shinyhub/commit/04a28decaa43361da0c7f54334a7445a59d5ff78))
- **fargate**: instrument RunOnce with RunTask counter and latency metrics ([4bbd22b](https://github.com/rvben/shinyhub/commit/4bbd22b4c4b03d8410a535a30e7a19fffbe2dd9d))
- **metrics**: implement FargateMetrics on Registry and wire to Fargate runtime at startup ([d494ee5](https://github.com/rvben/shinyhub/commit/d494ee50c46f23b1b7c200c856ace4b59c238653))
- **fargate**: emit startup Warn when route_via_public_ip is enabled ([95ecef2](https://github.com/rvben/shinyhub/commit/95ecef29e7731f3b8fa0c2d4cefba7387cb816d1))
- **fargate**: add Inventory error counter and structured slog calls across lifecycle paths ([340ea44](https://github.com/rvben/shinyhub/commit/340ea44aa91b12e105dc74b1969222c3eea4f50c))
- **fargate**: record WaitIPTimeout and StopTask counters ([6aee215](https://github.com/rvben/shinyhub/commit/6aee2152d1bb5cfc402897074118b030ea6def13))
- **fargate**: record RunTask counter and latency on Start ([14b0aa1](https://github.com/rvben/shinyhub/commit/14b0aa10b61e8403c3d3e968ab38e38e2c132fa2))
- **fargate**: add FargateMetrics interface with no-op default and SetMetrics wiring ([a8d53ee](https://github.com/rvben/shinyhub/commit/a8d53ee2c53892a554db3b954445e9e01813e518))
- **fargate**: add ClientToken on RunTask for control-plane retry idempotency ([bab44a7](https://github.com/rvben/shinyhub/commit/bab44a7b91ef12d85e291b080bc86ff48087786f))
- **runtime**: add ECS/Fargate runtime backend ([11316a8](https://github.com/rvben/shinyhub/commit/11316a81331e8fa09c1aa3a0809515f3de0291a6))
- **ui**: editable autoscale form on the Configuration tab ([3098843](https://github.com/rvben/shinyhub/commit/30988432cada544864e40c7139bd071cc8f4a15a))
- **ui**: show live pool vs autoscale bounds and flag drift ([bcd0993](https://github.com/rvben/shinyhub/commit/bcd099399badec8466879898434358ca00a14531))
- **ui**: surface autoscale state and reject rollup on overview ([1d5db92](https://github.com/rvben/shinyhub/commit/1d5db9250b977cf7d3be2d44cc24c8be03df9414))
- **cli**: manage and display app autoscaling ([3c3d6a2](https://github.com/rvben/shinyhub/commit/3c3d6a23a0ca6475568663e661265139618fb6ca))
- **cmd**: wire the replica autoscale controller behind a runtime switch ([e9f102d](https://github.com/rvben/shinyhub/commit/e9f102d8dcc11d8cc8cc98864ffda3f598e6819f))
- **autoscale**: provider-agnostic replica autoscale controller ([0e4a304](https://github.com/rvben/shinyhub/commit/0e4a304cbb069cc0c4935001360496ef801aecc6))
- **db**: list autoscale-enabled running and degraded apps ([ce1fac4](https://github.com/rvben/shinyhub/commit/ce1fac4397b2097a03ed33d4e0def9b955ec2679))
- **api**: accept and surface per-app autoscale settings ([60d646e](https://github.com/rvben/shinyhub/commit/60d646eb322ba555b4b121b6a07a332c140dfc52))
- **config**: add global replica autoscale controller settings ([bf0b39b](https://github.com/rvben/shinyhub/commit/bf0b39b8fc036830f733445e7bc690115b411071))
- **db**: persist per-app autoscale configuration ([1948e0d](https://github.com/rvben/shinyhub/commit/1948e0dc593d139833e85bf21bcabebbd77a807f))
- **api**: add incremental ScaleUp/ScaleDown replica primitives ([fc8c370](https://github.com/rvben/shinyhub/commit/fc8c37092e7f3c785ce01ff1254d8d2a7c33759e))
- **proxy**: add graceful drain so a slot sheds new sessions but keeps sticky ones ([e950f37](https://github.com/rvben/shinyhub/commit/e950f37c04b15a5827600c6bd313815b0e494b64))
- **api**: pin shared-mount consumers to their source's worker ([7d70703](https://github.com/rvben/shinyhub/commit/7d707036a60610e1fee8d8059b2fa70c28f53797))
- **api**: reject shared mounts on multi-worker tiers ([a9e08f4](https://github.com/rvben/shinyhub/commit/a9e08f4d438ff134be863116826a8d286ddf0803))
- **worker**: route replicas across multiple workers per tier ([15d094c](https://github.com/rvben/shinyhub/commit/15d094c9dc56b5742f5ad46a07363f6f715ce23a))

### Fixed

- **test**: repair real-ECS integration test build + guard gated tests in CI ([45dcab8](https://github.com/rvben/shinyhub/commit/45dcab8a699e2a3170c39278541d214cc98dd47e))
- **ui**: serve the SPA shell for direct loads of /workers ([00ab260](https://github.com/rvben/shinyhub/commit/00ab26003a5e2f75e9a2d97995476e129f0843fc))
- **test**: correct stale log assertion in remote-worker e2e ([6420d41](https://github.com/rvben/shinyhub/commit/6420d41239872784e348f866ccd0a036681176f2))
- **lifecycle**: use IsECSManagedWorkerID in workerDeclaredGone and partial-adopt gate to support EC2 launch type ([0f0b5a4](https://github.com/rvben/shinyhub/commit/0f0b5a441bbbd478b5aef51ed8ce8c5c80d42b04))
- **config**: guard DefaultResourcesForApp against nil app ([957fa47](https://github.com/rvben/shinyhub/commit/957fa472450ba79bf1e108ff07cc4af460ede8ee))
- **config**: resolve resource defaults from app placement tier ([47fe438](https://github.com/rvben/shinyhub/commit/47fe438573e1702f24e4ca8618631e1d2642cce9))
- **api**: eliminate chi/TimeoutHandler data race in Observe middleware ([96188a7](https://github.com/rvben/shinyhub/commit/96188a7cae106235546763b29e1b7bbad8315d3c))
- **ui**: distinguish undefined metrics_available (pending) from false (PID-less) in metricsText to prevent n/a flash on initial load ([ec31e09](https://github.com/rvben/shinyhub/commit/ec31e096712acef6e039b8a0d8665db1fe1d16c3))
- **ui**: use underscore CSS selectors for autoscale badge classes to match JS-generated class names ([c52fba0](https://github.com/rvben/shinyhub/commit/c52fba052de5f51d5078b34a1701650960e97d63))
- **ui**: escape backend label in replica panel innerHTML to prevent XSS from operator-controlled tier/provider values ([de1c7c5](https://github.com/rvben/shinyhub/commit/de1c7c572db22b1bf07aae06200f981e44f14405))
- **ui**: prevent kill-switch warning accumulation on repeated renderAutoscaleSummary calls and guard formatRelative against future timestamps ([e049533](https://github.com/rvben/shinyhub/commit/e0495339f6cdd3bf2a2b4811f416e8aed3c76ccf))
- **api**: metrics_available false when sample fails; remove misleading omitempty; add clarifying comments ([376b0aa](https://github.com/rvben/shinyhub/commit/376b0aa271c141169c902f100a9dd4b2b809b1a0))
- **bundletoken,config**: remove unused ErrDigestMismatch and fmt import; reject non-positive bundle_token_ttl env value ([d2bd1b9](https://github.com/rvben/shinyhub/commit/d2bd1b927b650f927fcf568e580819c4d8bf21fd))
- **runner**: set -eu, explicit sha256sum error handling, run as non-root user ([13c7f96](https://github.com/rvben/shinyhub/commit/13c7f966303b73898575a960096a26a6772a5582))
- **fargate**: return PartialInventoryError on per-batch DescribeTasks failure ([bf15dd4](https://github.com/rvben/shinyhub/commit/bf15dd41d34f5c78a00461d93372ad3bc83508ea))
- **lifecycle**: two-phase remote replica adoption for Fargate tasks ([e82f970](https://github.com/rvben/shinyhub/commit/e82f970ad55a9d64827041f9b09df1774ac8b023))
- **lifecycle**: do not declare fargate replicas gone on missing worker row ([5102914](https://github.com/rvben/shinyhub/commit/5102914b58ed0c8beb4ae98e84cca9bcd18075af))
- **fargate**: report PROVISIONING/PENDING tasks as Running in Inventory ([c9a9a88](https://github.com/rvben/shinyhub/commit/c9a9a88d99f43c0935c7afc68d9c4f91695e4bb9))
- **config**: numeric env-var overrides return errors on non-integer input ([cc25b04](https://github.com/rvben/shinyhub/commit/cc25b0404d846b471ddc9e9c54fdf72505bb7d22))
- **fargate**: align management tag to shinyhub.managed=true ([9c7ca42](https://github.com/rvben/shinyhub/commit/9c7ca4263f4b3fac3417a049897fc57518e0b241))
- **serve**: sampler selection reads resolved default-tier mode, not legacy Mode field ([3e5e4ef](https://github.com/rvben/shinyhub/commit/3e5e4efea11b709b1f768edc6a18ee5f0a82d16f))
- **fargate**: validate non-empty slug in RunOnce, consistent with Start ([ea10797](https://github.com/rvben/shinyhub/commit/ea107975a95ea6a393f24e9f347e3f6d9b96997d))
- **fargate**: use atomic.Int32 for polls counter to eliminate data race in TestRunOnceCancelsViaSleepError ([6410303](https://github.com/rvben/shinyhub/commit/64103032dff483125253fd089cf98979ab5732ea))
- **fargate**: report PROVISIONING/PENDING tasks as Running=true in Inventory ([356b275](https://github.com/rvben/shinyhub/commit/356b275c93dbdfcde8d45b54b60526b1745d02c6))
- **fargate**: validate non-empty slug in Start and always emit SHINYHUB_SLUG ([e962cca](https://github.com/rvben/shinyhub/commit/e962ccae64eb67fd18d52320ac328aef1fd00f6d))
- **fargate**: use 30s timeout context in Signal to bound hung StopTask calls ([1e22e51](https://github.com/rvben/shinyhub/commit/1e22e5134dbd4f841d03556aed0682b51f2ceb1a))
- **fargate**: round CPU unit conversion instead of truncating ([fb7675a](https://github.com/rvben/shinyhub/commit/fb7675accdc0f303770cee62e8c98571590f52df))
- **fargate**: fail fast on non-MISSING describe failures instead of polling to timeout ([8e1429f](https://github.com/rvben/shinyhub/commit/8e1429f0c229d809b5d470ec0cb5c1767f9d9bf1))
- **ui**: refuse fractional autoscale bounds instead of silently truncating ([bca4763](https://github.com/rvben/shinyhub/commit/bca47635509cbcc02c7460a68fcd6e6eb6470596))
- **proxy**: shed cookie-less requests when every backend is draining ([8b557ab](https://github.com/rvben/shinyhub/commit/8b557abb882106209c878fd3693c9d4d8dafa638))
- **api**: roll back started replica when scale-up persistence fails ([b363ba1](https://github.com/rvben/shinyhub/commit/b363ba19407c53482a5c3a8f9a06e1f982bc7095))
- **recovery**: keep app reconcilable after marking a down worker's slot lost ([b53a0d0](https://github.com/rvben/shinyhub/commit/b53a0d077f78ce2eb9360d9b298324985d292f93))
- **recovery**: mark replicas on down sibling workers lost ([7048916](https://github.com/rvben/shinyhub/commit/7048916aca1e51118179347d6decd7f29dd2a095))
- **lifecycle**: only heal an unreachable worker's replica once it is declared down ([c2c7672](https://github.com/rvben/shinyhub/commit/c2c7672491a8a4adae6b48b8d62bb973eba3ef57))
- **api**: validate autoscale bounds against the stored range even when disabled ([89b1792](https://github.com/rvben/shinyhub/commit/89b17929977103d4f86f762fcb45e8a42b39c810))
- **api**: re-enforce per-app autoscale bounds under the deploy lock ([444c37b](https://github.com/rvben/shinyhub/commit/444c37b6c199ef9bf17d6ffeb5f1e6ce4bf7296a))
- **autoscale**: do not scale down a degraded pool ([35f7fbf](https://github.com/rvben/shinyhub/commit/35f7fbfa23781b0432035589f63d7c9ca3912f3a))
- **config**: reject non-positive autoscale scan interval and cooldown ([5514e67](https://github.com/rvben/shinyhub/commit/5514e678c9da47a6bbd380cb34c1f5d3de9f5d8e))
- **cmd**: bound autoscale reject window and drain controller on shutdown ([dba59b6](https://github.com/rvben/shinyhub/commit/dba59b69b3405311f35d212a46dab8455f97ea45))
- **autoscale**: guard invalid bounds, empty pools, and no-op cooldowns ([153523e](https://github.com/rvben/shinyhub/commit/153523ef29f8d1fbc75e7a61d8ea0d4f6369d04f))
- **api**: precheck shared-mount colocation before an env-restart tears down the pool ([bcd21da](https://github.com/rvben/shinyhub/commit/bcd21da0f830d892be1ecb625ed2e728e5c461dc))
- **process**: do not mark a replica stopped when its stop signal fails ([23fcb4e](https://github.com/rvben/shinyhub/commit/23fcb4ef7871dd0788d3ac2162d444df23cfb29d))
- **api**: do not pin a control-plane consumer for a multi-worker source ([556c80f](https://github.com/rvben/shinyhub/commit/556c80f9e36da3e8bff0ac9748b374fb6add2cdb))
- **api**: keep state intact when scale-down stop fails ([fbf5f19](https://github.com/rvben/shinyhub/commit/fbf5f19ece7c5379a7e671449d26e7b426c9b59d))

## [0.6.2](https://github.com/rvben/shinyhub/compare/v0.6.1...v0.6.2) - 2026-05-27

### Added

- **cmd**: wire fleet gauges, lifecycle metrics, and lifecycle tracing ([5acfb9a](https://github.com/rvben/shinyhub/commit/5acfb9a81283b720b7e61a11a536ee2f0622971d))
- **api**: structured access log with request-ID and deploy metrics ([0f2a856](https://github.com/rvben/shinyhub/commit/0f2a856e26fc4daeac694b80b65689212ee0257e))
- **servertrace**: add service.instance.id resource attr and tracer accessor ([a4328a2](https://github.com/rvben/shinyhub/commit/a4328a272ab6adc427f2793be4e1ea29dbf65a8d))
- **lifecycle**: instrument background ops with metrics, spans, and logging ([6c9e0a8](https://github.com/rvben/shinyhub/commit/6c9e0a82de3ce3b8386e768412fc2e44e135fba5))
- **metrics**: add deploy, lifecycle, and fleet Prometheus series ([44a407d](https://github.com/rvben/shinyhub/commit/44a407d8f758a801743b7aa55ec37d4cce80d541))
- **cmd**: enable lost-replica healing and worker-aware monitor eviction ([9a35954](https://github.com/rvben/shinyhub/commit/9a3595419c174f64f2e946497eef779737e05f31))
- **cli**: render lost-replica reason in apps show ([50da8a7](https://github.com/rvben/shinyhub/commit/50da8a795d5eab128b07d49c296adc05b518bc69))
- **api**: derive worker-unavailable reason and evict on worker revoke ([0b8d3cf](https://github.com/rvben/shinyhub/commit/0b8d3cf79e98acb51b4bac29b586ed17f2088876))
- **lifecycle**: self-heal lost replicas and gate eviction on worker ownership ([55221d5](https://github.com/rvben/shinyhub/commit/55221d5ca16adfc5ca5836549540fa792fbcbedc))
- **db**: add ListReconcilableApps and derived replica reason field ([fccd6eb](https://github.com/rvben/shinyhub/commit/fccd6ebc208c3e30d6e4d3f901f5c9f05cde010d))
- **process**: add restart sentinels and worker-aware replica eviction ([2d7f8db](https://github.com/rvben/shinyhub/commit/2d7f8db8da023e4e99ce127c944d4a11ae6cfed7))
- **worker**: tombstone long-dead worker rows ([123c0f8](https://github.com/rvben/shinyhub/commit/123c0f8caf3838c30704c12e936bc211bb342f14))
- **worker**: explicit worker revocation ([efbadfd](https://github.com/rvben/shinyhub/commit/efbadfde4e6552d6cfd87b2a14e242a33c75ce9e))
- **worker**: re-adopt persisted identity and hot-reload CA trust with renewal escalation ([baf0ba0](https://github.com/rvben/shinyhub/commit/baf0ba059a8e6fa29dc72c53701a98b1fea51f13))
- **worker**: renew worker certs on heartbeat with TLS hot-reload ([b467509](https://github.com/rvben/shinyhub/commit/b467509edf8a44cbd441bbadafd1b4b5bbc2fe2f))
- **worker**: rotate control-plane client cert past its half-life ([e146e7f](https://github.com/rvben/shinyhub/commit/e146e7f84f6f261c51884c7ef0ddd46821465c8d))

### Fixed

- **ui**: size deployments version column to fit epoch-millis IDs ([c2c9ca9](https://github.com/rvben/shinyhub/commit/c2c9ca9267db0e57be026065b403b6c4363452e1))
- **observability**: route proxy and deploy errors through structured slog ([2fd5e30](https://github.com/rvben/shinyhub/commit/2fd5e304332d8b27750bd05ff08c7f61214c8dc1))
- **worker**: require join token only for fresh registration, not re-adoption ([3b1d47a](https://github.com/rvben/shinyhub/commit/3b1d47a9985e8399c85ff6bbab4e3e430886c677))
- **api**: serve the worker fleet view from the authoritative store ([9fd8ed0](https://github.com/rvben/shinyhub/commit/9fd8ed07ad23821366ac9203f3e6623cf2da2acd))
- **worker**: return a copy from the rotating certificate provider ([cd2f8fa](https://github.com/rvben/shinyhub/commit/cd2f8fabeb2841465a7739f08f8fd3b1b2a686b7))
- **lifecycle**: persist live endpoint when re-adopting a remote replica ([1e16239](https://github.com/rvben/shinyhub/commit/1e162398246e9524bd6dcb94bab202d08d52f800))
- **proxy**: gate worker-loss deregister on the live route still pointing at the lost replica ([86f6fc0](https://github.com/rvben/shinyhub/commit/86f6fc0d1aaa8600b17ef2ec0ececfae9434f5e5))
- **lifecycle**: guard replica-loss transition on current worker ownership ([c10dd31](https://github.com/rvben/shinyhub/commit/c10dd310fd11024e81c8b9b6f177463045ba82ca))
- **worker**: evict replicas on revoke and serialize revoke against heartbeats ([0c023c4](https://github.com/rvben/shinyhub/commit/0c023c49508f7f3ac6a9f1b5c9e3729f06fbdea1))
- **worker**: rotate the worker-API listener certificate ([087fe02](https://github.com/rvben/shinyhub/commit/087fe0280387dbb21cc024820c1de457f1031080))
- **api**: adopt self-evicting limiter for worker register throttle ([a9f659f](https://github.com/rvben/shinyhub/commit/a9f659f4a6f64b00a7212507911ae1edf12df20b))
- **worker**: bind inbound listener before the up-front heartbeat ([efe5b91](https://github.com/rvben/shinyhub/commit/efe5b91d89127423a093c90551d8d7051ac753f3))
- **worker**: keep single-tier ownership on heartbeat and renew re-adopted certs promptly ([65e5e6a](https://github.com/rvben/shinyhub/commit/65e5e6a4a98ff488379ab698599309785a291619))

## [0.6.1](https://github.com/rvben/shinyhub/compare/v0.6.0...v0.6.1) - 2026-05-27

### Added

- **cli**: render rejects-by-reason rollup in apps show ([62a19d0](https://github.com/rvben/shinyhub/commit/62a19d011f30fb782946a9dc3536f5cb27419636))
- **api**: surface rejects_by_reason rollup in app detail envelope ([9b476d7](https://github.com/rvben/shinyhub/commit/9b476d7c77dbbb416a3d7577f758f0a65d4ee28c))
- **cmd**: wire admission-reject metrics and log the reject reason ([6b4d0d7](https://github.com/rvben/shinyhub/commit/6b4d0d76602626bdac864a8f8aa30eb9511e60b5))
- **metrics**: add shinyhub_admission_rejects_total counter ([6a8b41a](https://github.com/rvben/shinyhub/commit/6a8b41a740d2091b3c4b153e5ff3d8a3e5d18e42))
- **api**: forget reject history when an app is deleted ([3d0a8e7](https://github.com/rvben/shinyhub/commit/3d0a8e7abc7a9b5938b1881795506a93aa36b0f8))
- **proxy**: emit X-Shinyhub-Reject on saturation, unknown-slug, and readiness rejections ([7ae36b5](https://github.com/rvben/shinyhub/commit/7ae36b5d2364e9040c64910585f7a7eed5e2631e))
- **proxy**: add recordReject helper, recorder interface, and rollup accessors ([3e475c3](https://github.com/rvben/shinyhub/commit/3e475c3e4b492bcde1bbe513db61f8b327918999))
- **proxy**: add RejectReason vocabulary and rolling reject counter ([db20283](https://github.com/rvben/shinyhub/commit/db20283d501eac87309c19d8ac6fecb6d938a7b4))
- **worker**: pin control-plane CA at join and run the worker standalone ([f564e17](https://github.com/rvben/shinyhub/commit/f564e1789860a7a748a1f4a71f5b4dfe702e4045))
- **cmd**: start worker-down monitor at control-plane startup ([b54ac40](https://github.com/rvben/shinyhub/commit/b54ac4041ff6d152357a16b9824d18c5169877c3))
- **lifecycle**: mark stale workers down and transition their replicas to lost ([82e8967](https://github.com/rvben/shinyhub/commit/82e89679bb238e185fd7247f19ca3969da8afe13))
- **lifecycle**: reconcile remote replicas by deployment id via agent inventory ([43aea19](https://github.com/rvben/shinyhub/commit/43aea19ae17a82860b0bc6c076bdc5a540cdaa51))
- **worker**: re-adopt replica containers and rebuild data-plane routing on agent restart ([c649e8a](https://github.com/rvben/shinyhub/commit/c649e8a3a7e24ecc84b80dce7ff96c1760bf3dee))
- **worker**: add agent inventory API and remoteRuntime inventory capability ([a61cc60](https://github.com/rvben/shinyhub/commit/a61cc60e5a77430ce3a3b91fb90d93d7b3eb84be))
- **process**: stamp deployment_id, app_version, content_digest on replica containers ([e5147dd](https://github.com/rvben/shinyhub/commit/e5147dd87ce22808311ad315b1ba51e06be727c0))
- **db**: add lost replica status and status index (migration 021) ([70c4fb3](https://github.com/rvben/shinyhub/commit/70c4fb3806befe849ab98831592e0eddac945390))
- **cmd**: register remote runtimes for remote_docker tiers and build CP mTLS dialer ([d9d3cff](https://github.com/rvben/shinyhub/commit/d9d3cff9c26c775d39c9b881d8cd766ddf2a3fa0))
- **jobs**: route jobs to the lowest-indexed tier with remote-aware params ([e890f55](https://github.com/rvben/shinyhub/commit/e890f55b0bc86f93750da89d43aa5a41bd8f9c63))
- **deploy**: reject cross-node shared mounts ([8f9482f](https://github.com/rvben/shinyhub/commit/8f9482fd3031d5f8694b610d1e1256ba7849720a))
- **worker**: add HTTP/1.1 mTLS AgentDialer for control-plane-to-worker calls ([c656e95](https://github.com/rvben/shinyhub/commit/c656e95c43c4f3d5889cb886c5130120cefc0749))
- **worker**: add CA.ControlClientCertificate for control-plane mTLS auth ([03526a9](https://github.com/rvben/shinyhub/commit/03526a98de383a4ce8fe4c846f5afdca66cb1bb8))
- **config**: accept remote_docker tier mode and guard buildRuntime ([308c10f](https://github.com/rvben/shinyhub/commit/308c10f3764ebb2da6608a6766ffcb6ab4d7f12b))
- **deploy**: make health checks and replica registration transport-aware ([619c26c](https://github.com/rvben/shinyhub/commit/619c26c1755aab642b82442c0402650ad3ef4ce4))
- **proxy**: support per-worker transport and tunnel path join; add Manager.TransportForTier ([9df3792](https://github.com/rvben/shinyhub/commit/9df3792e431418b165e4c776a68a2dbdaea9b57e))
- **process**: guard tier runtime registry with RWMutex and add removeRuntime ([51f0222](https://github.com/rvben/shinyhub/commit/51f0222f9142a7bf80552f1b3ce8ca23c286a2d9))
- **worker**: add registry-bound remoteRuntime over the agent mTLS tunnel ([cde8e1f](https://github.com/rvben/shinyhub/commit/cde8e1f70110a3afa3cd43c1edf4c781c6e8dfc3))
- **worker**: add agent inbound mTLS HTTP server serving replica-control API ([13b7dc1](https://github.com/rvben/shinyhub/commit/13b7dc1149994a6a7b219a61825a3f7c43d28928))
- **worker**: add data-plane reverse proxy and Routes method for replica-control API ([9c6cf11](https://github.com/rvben/shinyhub/commit/9c6cf113b9486573a84cbae3b2be01a99d51e7e7))
- **worker**: add replica Signal, Wait, Stats, and RunOnce handlers ([d613f73](https://github.com/rvben/shinyhub/commit/d613f73946f4f34c0db1deb39229c5bab471ed2f))
- **worker**: stream replica Start as NDJSON result-then-log frames ([95b48d9](https://github.com/rvben/shinyhub/commit/95b48d91ae4895115b990c2671f5629a41d1ba37))
- **worker**: add replica server with injectable port allocator and app-data provisioning ([6a3b80d](https://github.com/rvben/shinyhub/commit/6a3b80dff741dd9d704612c06831bf61389fe8fc))
- **worker/api**: add replica-control wire types ([c639897](https://github.com/rvben/shinyhub/commit/c63989736d63c5a9797006d6335898d56e3ad31a))
- **process**: split in-container bind port from host publish port ([7c21a04](https://github.com/rvben/shinyhub/commit/7c21a0416329a23183331b86e2b2133e33f33a51))
- **process**: add HostProvidesAppData runtime capability and gate host app-data provisioning ([9ef3d4b](https://github.com/rvben/shinyhub/commit/9ef3d4bf889f71c83b8f0d1624acc1852d533aaa))
- **jobs**: carry deployment metadata on scheduled RunOnce params ([3e55bd0](https://github.com/rvben/shinyhub/commit/3e55bd02791f604fd00b1b77e0a3ab4df20cccde))
- **deploy**: thread deployment metadata through every launch path ([c9313cf](https://github.com/rvben/shinyhub/commit/c9313cf8f990d928a84b25256fe7ef0bed910e21))
- **deploy**: carry content_digest, deployment_id, app_version into StartParams ([218e356](https://github.com/rvben/shinyhub/commit/218e356c14ba331869e2cc4490f6a56e9738e751))
- **worker**: add worker subcommand and wire control-plane mTLS worker API ([5a2de0d](https://github.com/rvben/shinyhub/commit/5a2de0d6074c7c152f04cb1ebd66822101fff615))
- **worker**: agent bootstrap (CSR join), identity persistence, heartbeat loop ([dbce5f8](https://github.com/rvben/shinyhub/commit/dbce5f89431ca58cefb87413b439e5ee422f26ae))
- **worker**: bundle cache with digest verification and atomic install ([aee4ef3](https://github.com/rvben/shinyhub/commit/aee4ef39de4883b1e595d9dd83ba09264e72e1dd))
- **worker**: agent mTLS client (register, heartbeat, bundle fetch) ([e4b16ba](https://github.com/rvben/shinyhub/commit/e4b16ba9e97caba06cd519097951a4108211fa80))
- **api**: bundle fetch endpoint resolving content digest to stored zip ([14a4cac](https://github.com/rvben/shinyhub/commit/14a4cacd6100637b74d353b8ebe58511e47b49c1))
- **api**: worker register and heartbeat handlers with cert-identity auth ([b9ac269](https://github.com/rvben/shinyhub/commit/b9ac269362bb58e27e656a760237cdd545e92de1))
- **config**: add control-plane worker config block ([e773123](https://github.com/rvben/shinyhub/commit/e773123b771051cfa08a2b8a16be32fcde2ffa71))
- **worker**: DB-backed worker registry with in-memory routing index ([4bf8966](https://github.com/rvben/shinyhub/commit/4bf8966a1e5d5bde7ca5a497c59e7b07651e8287))
- **worker**: internal CA with node-id-bound short-lived client certs ([6994c54](https://github.com/rvben/shinyhub/commit/6994c54dab59ad68b636f648f887b3a016f9d79e))
- **worker**: define worker/control-plane wire types ([e574888](https://github.com/rvben/shinyhub/commit/e5748882d7223967d1f515918843b18ef28a6091))
- **db**: add workers table and worker registry queries ([3b80f62](https://github.com/rvben/shinyhub/commit/3b80f6281916f869109186e9f3a3dac3335a2a21))
- **db**: expose content_digest on deployment read paths and add GetDeploymentByDigest ([c5a7ded](https://github.com/rvben/shinyhub/commit/c5a7ded68e65bd9658339d04c559aa07d7b7fa8a))
- **cli**: add --tier name=count placement flag to apps set ([6b1658d](https://github.com/rvben/shinyhub/commit/6b1658dacbde7076eb82776a2734aacef970d7e9))
- **api**: accept per-tier replica placement on app PATCH ([ae67c67](https://github.com/rvben/shinyhub/commit/ae67c677331d2ba4aea8aed67353c6575fbe9403))
- **lifecycle**: route process recovery per replica tier via the runtime registry ([fe8392f](https://github.com/rvben/shinyhub/commit/fe8392fb4e98f6eacf8d7930312b809939d2ec55))
- **runtime**: build and register a runtime per configured tier ([ee12fee](https://github.com/rvben/shinyhub/commit/ee12feed19a4b0161e7f8a72f1d62fe09cd8d1b6))
- **process**: tier-aware container labels via dockerLabels helper ([f01d7d5](https://github.com/rvben/shinyhub/commit/f01d7d5b5ece59af7bf1d7166d8b4b9ac19fe782))
- **deploy**: placement-driven per-tier replica boot ([d82b588](https://github.com/rvben/shinyhub/commit/d82b588dfcda6f259988196270db98020a49d885))
- **process**: per-tier Manager capability methods ([731c837](https://github.com/rvben/shinyhub/commit/731c837fb127dbd7e130ead497e1d5c1be8d85c8))
- **db,process**: carry deployment_id on replicas for reconcile ([df8e3c4](https://github.com/rvben/shinyhub/commit/df8e3c416994a80d885293739f65acd1e9496db2))
- **db**: persist per-app replica placement with resolved total ([c6cfd31](https://github.com/rvben/shinyhub/commit/c6cfd31bb2c9a53fdb2264a9bfadbbd80f001f4d))
- **process**: add deterministic ExpandPlacement tier allocator ([51ee10d](https://github.com/rvben/shinyhub/commit/51ee10d9e8d436532893054947d92604cb37d158))
- **config**: add ordered runtime.tiers with validation and helpers ([d510817](https://github.com/rvben/shinyhub/commit/d51081716b603fe494a46027be290b90ca9df79d))
- **lifecycle**: persist replica endpoint metadata and recover by stored endpoint URL ([e4c5769](https://github.com/rvben/shinyhub/commit/e4c576939b725190664fea5a90355fe9c3af49c5))
- **deploy**: health-check and register replicas by runtime-returned endpoint URL ([8d897b4](https://github.com/rvben/shinyhub/commit/8d897b495950dafdb7c95116e006440489bf70c3))
- **deploy**: provision app data via LocalVolume and publish bundles via BundleStore ([15644ca](https://github.com/rvben/shinyhub/commit/15644ca0f705cc6081423d6b291522ea172228ce))
- **storage**: add BundleStore and DataVolume interfaces with local implementations ([903a534](https://github.com/rvben/shinyhub/commit/903a534de4c6936336d1ebb46e0a9f4b8d062da8))
- **db**: persist per-replica provider, tier, endpoint URL, worker id, version, desired state ([cfd8f20](https://github.com/rvben/shinyhub/commit/cfd8f2049d96354c88b3d63064eda799e342d781))
- **process**: Runtime.Start returns ReplicaEndpoint with route URL and worker identity ([bc96974](https://github.com/rvben/shinyhub/commit/bc9697422b5eb4f0e59c214f846e6e908bd50a81))
- **process**: replace single runtime with tier-keyed registry ([549f982](https://github.com/rvben/shinyhub/commit/549f982b2fabe86ed9eddddcd2f3909fd245a6a4))

### Fixed

- **proxy**: return 404 for unknown app slugs instead of a cold-start 503 ([a74e571](https://github.com/rvben/shinyhub/commit/a74e5719f364557b3e229b06220ee3b3b5552f96))
- **worker**: stop streaming replica logs once the request handler returns ([89e6af4](https://github.com/rvben/shinyhub/commit/89e6af4cf75808a73cf0aa74b70814972f5d1fe2))
- **worker**: exclude downed and superseded workers from tier routing ([d8a9872](https://github.com/rvben/shinyhub/commit/d8a9872dcc6d67696848bb5358c4b4eb98737565))
- **worker**: pull and mount app bundle on remote replica start ([e4ab362](https://github.com/rvben/shinyhub/commit/e4ab36279114d11113899848a59176854710493a))
- **db**: apply post-baseline migrations to pre-ledger databases ([ab97d36](https://github.com/rvben/shinyhub/commit/ab97d365a9d2cd829d88521a1ff8d38056630089))
- **api**: reject manifest replicas change when app uses tier placement ([d171c9d](https://github.com/rvben/shinyhub/commit/d171c9dfc50752db90d10b01e81349e29047a256))
- **lifecycle**: strengthen endpoint-URL recovery test and collapse docker lookup loop ([bbd7d46](https://github.com/rvben/shinyhub/commit/bbd7d46074a18296118124f5040fb042eb1a3558))
- **lifecycle**: populate AppVersion in watcher UpsertReplica calls ([3bb8bfe](https://github.com/rvben/shinyhub/commit/3bb8bfeaac6371cf9a0aacbff94e676b8cb6dffd))
- **deploy**: handle malformed endpoint URL in waitHealthy; strengthen endpoint test ([ae34304](https://github.com/rvben/shinyhub/commit/ae34304bb62c6583b95bd57fb55f1fe5eac8632d))

## [0.6.0](https://github.com/rvben/shinyhub/compare/v0.5.8...v0.6.0) - 2026-05-23

### Added

- **tracing**: emit OTel server spans for control-plane API requests ([2968b3c](https://github.com/rvben/shinyhub/commit/2968b3c1069836032aa7254a1b4c598eb2a8a95f))
- **metrics**: add Prometheus self-telemetry for the server process ([2635c14](https://github.com/rvben/shinyhub/commit/2635c145153db158239da082dd717618b7eb017f))

### Fixed

- **metrics**: observe API telemetry at the timeout-handler boundary ([3732af5](https://github.com/rvben/shinyhub/commit/3732af556fb1ae39c0c096797f9484b1c105b595))

## [0.5.8](https://github.com/rvben/shinyhub/compare/v0.5.7...v0.5.8) - 2026-05-23

### Fixed

- **api**: wait for the deploy lock on redeploy so a replica change is never dropped ([fedb44f](https://github.com/rvben/shinyhub/commit/fedb44f913643d14d9d0e37ad377315be2e3590c))
- **api**: refcount redeploy-in-flight so an unrelated lock holder cannot wedge it ([9f872f0](https://github.com/rvben/shinyhub/commit/9f872f0e737d63417ca01b576982adb7178c6d01))
- **cli**: surface hooks-skipped warning on the fleet deploy path ([9e21c79](https://github.com/rvben/shinyhub/commit/9e21c795e5fd2d58c59e40fa9f9f2262899e2463))
- **api**: advertise redeploy-in-flight so apps set --wait polls the new pool ([d962207](https://github.com/rvben/shinyhub/commit/d9622075b8fb3a895b35640154b8ef023b0b1f5c))
- **cli**: fall back to app cap when server omits effective max sessions ([b4e923a](https://github.com/rvben/shinyhub/commit/b4e923a57e28ac8c2db6cbaabe52988f5359f71a))
- **cli**: surface the last poll error when deploy --wait loses contact after a stale status ([d83a88b](https://github.com/rvben/shinyhub/commit/d83a88b3c87829f83ca84f31f7cdf5a0f6b84fdc))
- **cli**: reject nonexistent or non-directory paths in manifest validate ([946433b](https://github.com/rvben/shinyhub/commit/946433be73d5ac1fd18ea6b65e9e5a4e785bc364))
- **cli**: only note disabled-schedule override after the run is accepted; detect cmd/cmd-json conflict by presence ([2fb9ee1](https://github.com/rvben/shinyhub/commit/2fb9ee1c60f05320bf0e094b73a4578b5f3a8eb3))
- **tracing**: combine split tracestate header values at the proxy hop ([2bbce73](https://github.com/rvben/shinyhub/commit/2bbce73a6f5482c6a7d0736e1b04856f0980128e))
- **schedules**: detect interleaved DST double-fires and treat full-hour ranges as hourly ([cf495bb](https://github.com/rvben/shinyhub/commit/cf495bbaade353b4cf27c7c65b17580f2c7a7a4f))

## [0.5.7](https://github.com/rvben/shinyhub/compare/v0.5.6...v0.5.7) - 2026-05-23

### Added

- **serve**: add --config flag for the server config file path ([4c29aa4](https://github.com/rvben/shinyhub/commit/4c29aa42bf1635c06b7206d682dc0b8c57205211))
- **shared-data**: warn that native-runtime read-only is convention, not enforcement ([cc8a341](https://github.com/rvben/shinyhub/commit/cc8a341c46e1f61cf7fada5ea75708a8907210e9))
- **schedules**: surface DST fall-back double-fire advisory in API, CLI, and UI ([7446f1f](https://github.com/rvben/shinyhub/commit/7446f1f8b2ea3c1e5ac7462d9b052fc8082fe7f0))
- **deploy**: surface post-deploy hooks skipped under container runtime ([d913cb1](https://github.com/rvben/shinyhub/commit/d913cb1c9e92132f1a8a4bfef51430f658d9d2ec))
- **cli**: confirm guard and --wait for apps set replica changes ([485a661](https://github.com/rvben/shinyhub/commit/485a6612fcb166f08dd2d24910114ae2fe4074d9))
- **cli**: surface session-expired hint on 401 with a JWT credential ([b9197e8](https://github.com/rvben/shinyhub/commit/b9197e8fcf8dd106cacf560bb65ecf942ac979b4))
- **apps**: show resolved session cap and admission ceiling in apps show ([dc10048](https://github.com/rvben/shinyhub/commit/dc10048abebc5c38557843ae08f6506df7f0e671))
- **cli**: clearer deploy help and a forgiving --wait default ([58858a4](https://github.com/rvben/shinyhub/commit/58858a4d181db3b31eae56042404f585a1e3e93f))
- **cli**: add manifest validate to check shinyhub.toml locally ([14b7693](https://github.com/rvben/shinyhub/commit/14b7693c14a2faf7c5671394ea18694aecb10e80))
- **cli**: add schedule update to edit a schedule in place ([1f19fc8](https://github.com/rvben/shinyhub/commit/1f19fc8e0599e39dda06e10d558a68ad18123670))
- **cli**: note when schedule run triggers a disabled schedule ([e325802](https://github.com/rvben/shinyhub/commit/e325802101c52323cc6d30c12dd295c3a8f368e5))
- **audit**: filter audit events by action via ?action= query param ([87542cf](https://github.com/rvben/shinyhub/commit/87542cf365a1a432ca67b6e8b7fd62bd1299f204))
- **tracing**: propagate W3C tracestate across the proxy hop ([30f05aa](https://github.com/rvben/shinyhub/commit/30f05aa683858ba9e3c9e4b151880d819c7a5670))
- **ui**: mark unsampled traces, date the When column, show poll freshness ([b1b0e56](https://github.com/rvben/shinyhub/commit/b1b0e56a163c7123df36631a640e0c8f612ad6af))
- **fleet**: add --health-timeout and per-app health progress lines ([cedb432](https://github.com/rvben/shinyhub/commit/cedb4324ad8a68d7c7524bb1d5fc3859539604d8))
- **fleet**: init annotates foreign-owned apps and handles empty fleets ([bc368da](https://github.com/rvben/shinyhub/commit/bc368dae40d0eef142b90220eea937bada98a12a))
- **fleet**: expose adopt_from in plan/apply JSON envelopes ([729fe1f](https://github.com/rvben/shinyhub/commit/729fe1fc99de7850c4be63b8331ea30a270e9b92))
- **fleet**: surface foreign-fleet ownership transfer on adopt ([201e0e9](https://github.com/rvben/shinyhub/commit/201e0e9a70e0ed1dca672e4a8c2583bb0aeb6d26))

### Fixed

- **ui**: refresh traces poll-freshness on empty and disabled polls ([a015b44](https://github.com/rvben/shinyhub/commit/a015b4484af690177c6e00b03edc002cacb318db))
- **tracing**: preserve 101 upgrade bodies so WebSocket tunneling works under tracing ([f3efe8a](https://github.com/rvben/shinyhub/commit/f3efe8aa409bf5071097f09fa94ba93d3aec418e))
- **tracing**: capture mid-stream upstream errors onto the span ([7fe58b0](https://github.com/rvben/shinyhub/commit/7fe58b037cdc6fc39f74b35dedd7fdfc0a053d94))
- **tracing**: populate span.Error on upstream proxy failures ([078d5b8](https://github.com/rvben/shinyhub/commit/078d5b8c34304676cb3885a9fa0a2d636c118e5d))
- **fleet**: suppress visibility warning for existing apps in fleet path ([3000ce8](https://github.com/rvben/shinyhub/commit/3000ce8a6956b69487e8dc2bbbd84d9f938caf89))
- **fleet**: route deploy progress to stderr in apply --json ([41bfb41](https://github.com/rvben/shinyhub/commit/41bfb41793a86b01956c608a2c1c72fea11e3dd7))
- **fleet**: strip DEL control char from managed_by comment ([45e22df](https://github.com/rvben/shinyhub/commit/45e22dfc37e43422695fb2abfba80bb2a9e059e7))
- **fleet**: sanitize server-controlled managed_by in init comment ([cf985e7](https://github.com/rvben/shinyhub/commit/cf985e796895e6bc8e690d76a9aa91a9269a93e2))
- **fleet**: do not duplicate parent flag-parse errors ([6b2e3c4](https://github.com/rvben/shinyhub/commit/6b2e3c4692db51327f6311a4d64088f417962c9a))
- **fleet**: keep flag-parse errors visible under SilenceErrors ([44d9cac](https://github.com/rvben/shinyhub/commit/44d9cacb8bdf30d81b2e4397bb5501d5f8bbed6f))
- **fleet**: validate git source scheme early, trim git stderr, dedupe error output ([2b79d82](https://github.com/rvben/shinyhub/commit/2b79d829be6d5b61727e22fb373c46ef1f6f6572))
- **fleet**: single combined apply suggestion, manifest-aware -f, glyph legends, dry-run header ([6138203](https://github.com/rvben/shinyhub/commit/6138203371017e7dddf9dffe7b144ab97a1374ed))
- **fleet**: correct apply failure hint and make adopt ownership crash-safe ([43181fd](https://github.com/rvben/shinyhub/commit/43181fd6de488e7b6b7de34f80dbceb014ad2a45))
- **ui**: hide schedule run exit code until the run has finished ([69d1267](https://github.com/rvben/shinyhub/commit/69d1267bf66fb9b47d06d39937424939c375429b))
- **schedule**: terminate live run-log follow at completion; honest run_id ([b33014f](https://github.com/rvben/shinyhub/commit/b33014fa457e9e5bd784c8f8d99441cfb675c735))
- **schedule**: honest exit codes and plain-text run logs ([49c663c](https://github.com/rvben/shinyhub/commit/49c663ca18a62529d7b98b5a9e1e3309e44b4204))
- **fleet**: mirror detailed-exitcode exit code in plan --json summary ([831a2c0](https://github.com/rvben/shinyhub/commit/831a2c070bca0b4adeabed9d8e4f49e2f881b1ca))
- **cli**: honor explicit --max-sessions-per-replica instead of -1 sentinel ([f5f4d34](https://github.com/rvben/shinyhub/commit/f5f4d34c35bd831f929584e3f24a151774bae6b2))
- **cli**: unwrap server {"error":...} envelope in failure messages ([7f2ac55](https://github.com/rvben/shinyhub/commit/7f2ac55372726d611d46ad5d93a24343a33c1e83))
- **shared-data**: make cycle check and insert atomic ([9e7e834](https://github.com/rvben/shinyhub/commit/9e7e834310892613c3f685a05fe7c251cb97abd5))
- **shared-data**: map grant/revoke errors to precise HTTP codes ([445823d](https://github.com/rvben/shinyhub/commit/445823d3b45d47735f76ffe82a5c44242a70f7b5))
- **config**: honor explicit-zero tracing knobs and validate strictly ([d0b48c3](https://github.com/rvben/shinyhub/commit/d0b48c36c77b43cb42e47b8bc98bdb4110cf873d))

## [0.5.6](https://github.com/rvben/shinyhub/compare/v0.5.5...v0.5.6) - 2026-05-22

### Added

- **ui**: show fleet ownership and live deployment digest on app detail ([e2699e4](https://github.com/rvben/shinyhub/commit/e2699e4e3b609cad4e531ce92a9a67b92f9e369e))
- **ui**: fleet ownership badge and segment filter on the apps grid ([bb1abc4](https://github.com/rvben/shinyhub/commit/bb1abc40299f1c7e7eb9f6842d1890cbbd4dea37))
- **ui**: add read-only fleet dashboard helper module ([fb60877](https://github.com/rvben/shinyhub/commit/fb6087788b0d3c9320c7ad457b9642eea686819c))
- **cli**: wire shinyhub fleet init command ([da6d0de](https://github.com/rvben/shinyhub/commit/da6d0de58421cc2c1864dd2bd79aa51a1accc2c3))
- **fleet**: manifest emitter and shared ValidFleetID ([fd364f0](https://github.com/rvben/shinyhub/commit/fd364f0096fd6312647652975a825c3954217589))
- **cli**: wire shinyhub fleet status command ([9c857da](https://github.com/rvben/shinyhub/commit/9c857da97a125bebae62a944eb2061ecd6581800))
- **cli**: fleet status human and quiet rendering ([8e214f7](https://github.com/rvben/shinyhub/commit/8e214f7e850e50366ac5ee556ea7854d63df307c))
- **cli**: fleet status data model and JSON envelope ([5eca9ff](https://github.com/rvben/shinyhub/commit/5eca9ff6b14c0c7e23f0df0996360c05d0b6a184))
- **cli**: shinyhub fleet apply command with confirmation and dry-run ([ffbcd20](https://github.com/rvben/shinyhub/commit/ffbcd20e2903d8b0ebcb0e48e84b5859498e9ad1))
- **cli**: fleet convergence engine with per-action preconditions and retry ([47aee8d](https://github.com/rvben/shinyhub/commit/47aee8dd9ab3cb302d07eb8dc214a35b8c5cd63e))
- **cli**: fleet apply result model, exit-code mapping and report ([7888e42](https://github.com/rvben/shinyhub/commit/7888e42ddc64620d7e6347b8e9b426bb2da62fba))
- **cli**: precondition-gated fleet patch, access and delete operations ([8060989](https://github.com/rvben/shinyhub/commit/8060989c391710c0b9abb6275a115836e2275eb0))
- **cli**: reusable single-app deploy with promoted-digest readback ([1443372](https://github.com/rvben/shinyhub/commit/1443372d2bca606c450015711253cb4ca88c585a))
- **cli**: fleet run correlation id and precondition request headers ([13f61f7](https://github.com/rvben/shinyhub/commit/13f61f7de056f4a669af662f5868912c7f552470))
- **cli**: fleet plan human+json rendering with exit codes and golden test ([bbaf557](https://github.com/rvben/shinyhub/commit/bbaf557c9ff32fc5f96152bb4a0c626b86773490))
- **cli**: fleet plan fetches state and computes the diff (read-only) ([5d180c7](https://github.com/rvben/shinyhub/commit/5d180c7584d36d365cbc16f1d954713e7aa14c45))
- **cli**: fleet command group + plan pre-flight (read-only skeleton) ([505e6a7](https://github.com/rvben/shinyhub/commit/505e6a74661fa63e5dba274301b622a9e3527801))
- **cli**: git source resolution with resolved commit and cleanup ([c5e814b](https://github.com/rvben/shinyhub/commit/c5e814be80b2a487acf861c0e5ab5556522d5dbc))
- **cli**: client local digest reusing the server bundler+hasher ([db32e85](https://github.com/rvben/shinyhub/commit/db32e85176a2c13eb946a0fc87e072c5656fbd78))
- **fleet**: pure order-independent reconcile diff engine ([cb3de65](https://github.com/rvben/shinyhub/commit/cb3de653124b600d00bc3978a17027cccb532472))
- **fleet**: source-form resolver wired into aggregated pre-flight ([4ba1eea](https://github.com/rvben/shinyhub/commit/4ba1eea18590aa208ebbf954d7ad416d5289b98e))
- **fleet**: strict fleet-manifest parser with aggregated validation ([262d55f](https://github.com/rvben/shinyhub/commit/262d55fecb779c774b2786017750c624e5de7405))
- **cli**: typed ExitCodeError honored by main for fleet exit codes ([c8e00ef](https://github.com/rvben/shinyhub/commit/c8e00ef0dc532a2251e4bf435b2837c72a652afc))
- **api**: add GET /api/server-info capability marker ([c1264fd](https://github.com/rvben/shinyhub/commit/c1264fd8aa610d4f56ce9c75fb5e880d77dd72fb))
- **api**: If-Match-style preconditions and managed_by patch on app endpoints ([d093154](https://github.com/rvben/shinyhub/commit/d093154c71f3e8d334820f2bdc765d512b36e861))
- **api**: expose content_digest and managed_by on the apps endpoints ([e5ca7f3](https://github.com/rvben/shinyhub/commit/e5ca7f3ad4d40bb5012c7e892471088c5612dbcb))
- **deploy**: record bundle content digest on the pending deployment ([4f3962f](https://github.com/rvben/shinyhub/commit/4f3962fe85fcab13308db856d624fcdc84b87fb6))
- **bundle**: deterministic content digest over accepted zip entries ([e60c8a8](https://github.com/rvben/shinyhub/commit/e60c8a87f0497822bc1f9c58db42fa06889285d8))
- **db**: add deployments.content_digest and apps.managed_by columns ([ee5be80](https://github.com/rvben/shinyhub/commit/ee5be80f635e0b1c97e3228158821be80b8391ac))

### Fixed

- **api**: bump updated_at on managed_by writes; clarify precondition semantics ([175b501](https://github.com/rvben/shinyhub/commit/175b50131482ec3d14683645036c76d60c885083))

## [0.5.5](https://github.com/rvben/shinyhub/compare/v0.5.4...v0.5.5) - 2026-05-18

## [0.5.4](https://github.com/rvben/shinyhub/compare/v0.5.3...v0.5.4) - 2026-05-18

### Added

- **ui**: apply injected branding to shell logo, footer, accent, and title ([ecd1450](https://github.com/rvben/shinyhub/commit/ecd14500393a8bc11293b58375348a7a6a90f88c))
- wire branding routes, branded /login, and landing page at / ([f78c0ae](https://github.com/rvben/shinyhub/commit/f78c0aee703ef5f4d321ab1b4a75c001f922088a))
- **api**: branding.json and apps.json handlers with anon public-only scoping ([3ea9ac9](https://github.com/rvben/shinyhub/commit/3ea9ac996766883a03d9b33475f705c05364f84e))
- **db**: ListPublicApps query for anonymous landing pages ([8ebe622](https://github.com/rvben/shinyhub/commit/8ebe6223972ec8ea96c8fdbc3cc1bf13652ee63a))
- **ui**: allow-list /branding asset handler ([cd9d3d4](https://github.com/rvben/shinyhub/commit/cd9d3d4f0c27129ac5ffda7ec36ac4d5b5273be8))
- **ui**: branding public object and script-safe index renderer ([a39eff5](https://github.com/rvben/shinyhub/commit/a39eff5710aa93e0fcbd982380ddb15133cf0bcf))
- **config**: fail-fast branding validation and asset resolution ([6ac9f78](https://github.com/rvben/shinyhub/commit/6ac9f78a4df74d1b44c99a10216e74d162031e2d))
- **config**: scalar env overrides for branding fields ([092fe45](https://github.com/rvben/shinyhub/commit/092fe45c792a31d6ddba44f73b95f5e35511a0be))
- **config**: add BrandingConfig types and YAML wiring ([9c1ad56](https://github.com/rvben/shinyhub/commit/9c1ad56ef671bc7415bc1715b81e93b738fbe4c6))

### Fixed

- **cli**: select auth scheme by JWT structure so opaque deploy tokens work ([3335fdc](https://github.com/rvben/shinyhub/commit/3335fdc30133a54e8aa76cdab5e1be1182dac857))
- **access**: retarget login redirect to /login so custom landing pages keep the auth flow ([7f74b91](https://github.com/rvben/shinyhub/commit/7f74b91329dd69c367504188d091c0a9a08cf29a))

## [0.5.3](https://github.com/rvben/shinyhub/compare/v0.5.2...v0.5.3) - 2026-05-18

### Added

- **scheduler**: per-schedule timezone for cron jobs ([fac27ab](https://github.com/rvben/shinyhub/commit/fac27ab9ed094af36c4ac0cf94691688b8f1e45c))

## [0.5.2](https://github.com/rvben/shinyhub/compare/v0.5.1...v0.5.2) - 2026-05-16

### Fixed

- **api**: apply app-settings PATCH atomically and roll back manifest on failed deploy ([9464a9f](https://github.com/rvben/shinyhub/commit/9464a9f4ca7820e99d68d5c7594963757c210775))
- **lifecycle**: bound wake-on-miss at shutdown and reconcile crashed replica slots ([c833552](https://github.com/rvben/shinyhub/commit/c833552d27513c8cbcdea82ff2dd14b233be8670))
- **api**: report admission outcome for manual schedule runs ([12e2dcd](https://github.com/rvben/shinyhub/commit/12e2dcd51e6c7acddc2b99e3cacda6f5f26a33fd))
- **config**: treat runtime default_max_sessions_per_replica=0 as unlimited ([cc2c3ff](https://github.com/rvben/shinyhub/commit/cc2c3ffeccb7f26a9da5e36a98a9310cfdc34ed2))
- **jobs**: drain in-flight scheduled runs on shutdown ([dea314c](https://github.com/rvben/shinyhub/commit/dea314cb6a6fcb1e9dfc40acbdf8236f9a964e8b))
- **server**: scope timeout-exempt API routes to exact method and path ([2edfcf3](https://github.com/rvben/shinyhub/commit/2edfcf357d30dd1c96c03e579fbedd0c3d670ac0))
- **api**: remove rejected deploy bundles before releasing the deploy lock ([e2f40bb](https://github.com/rvben/shinyhub/commit/e2f40bbcdb3552ce16f09df7deecd03594affd81))
- **db**: serialize concurrent OAuth provisioning with BEGIN IMMEDIATE ([4f20687](https://github.com/rvben/shinyhub/commit/4f20687dd76de8c48e3aa19495d461f6c14e20b0))
- **server**: exempt lifecycle mutations from the request timeout ([64f120a](https://github.com/rvben/shinyhub/commit/64f120a018922a1a5a0798728c1774d6af5f938e))
- **api**: bound rate-limiter memory growth ([672b126](https://github.com/rvben/shinyhub/commit/672b126be489a3eaa108ab29e8e986694f6b33db))
- **jobs**: bound scheduled-run concurrency and fail closed on undecryptable secrets ([d50988d](https://github.com/rvben/shinyhub/commit/d50988d8f4d7c330ff717887089d8d17990e9b4c))
- **auth**: provision OAuth and OIDC users atomically ([10b511a](https://github.com/rvben/shinyhub/commit/10b511ae0e0fd6f286a9a2fe191c74bd87c3b13e))
- **api**: harden app deploy and restart lifecycle ([4e0bbfb](https://github.com/rvben/shinyhub/commit/4e0bbfbf8424cfdb3ad783f6a299cac72c7f0949))

## [0.5.1](https://github.com/rvben/shinyhub/compare/v0.5.0...v0.5.1) - 2026-05-16

### Added

- **backup**: add shinyhub backup and restore commands ([2f1d6d4](https://github.com/rvben/shinyhub/commit/2f1d6d4952647191294308f712607200ae3d277c))
- **api**: broaden rate limiting coverage ([90f2884](https://github.com/rvben/shinyhub/commit/90f288402c048cfd14a37ea774225f30de3c95e8))
- **deploy**: durable deploy state machine ([1c562a7](https://github.com/rvben/shinyhub/commit/1c562a7ff343df987664c743563cdac81fcf3019))
- **server**: deliberate shutdown semantics for app processes ([8e68f47](https://github.com/rvben/shinyhub/commit/8e68f47a6ba54bf61e70be899ab2834b9539fad5))

### Fixed

- **backup**: reject self-containing output and preserve file modes ([34b2b34](https://github.com/rvben/shinyhub/commit/34b2b346c6d8a8e3129e9914f1e64f807149ed3d))
- **install**: enforce checksum and add cosign signature verification ([e61ec4f](https://github.com/rvben/shinyhub/commit/e61ec4f0972b2a95ace0bbb0c7fe88654b852a73))
- **docker**: add serve subcommand to Dockerfile entrypoint ([be91c3f](https://github.com/rvben/shinyhub/commit/be91c3faf5da054ad4a42a11f448ae8a1d2b896d))
- **api**: tombstone-ordered app deletion with startup reconcile ([f61f0ab](https://github.com/rvben/shinyhub/commit/f61f0abe784e3c142ebea1452139859044236e15))
- **process**: reap stopped Docker containers ([9f77edb](https://github.com/rvben/shinyhub/commit/9f77edb96c9710dddea203e91e91ed737dbba065))
- **data**: symlink-safe atomic path resolution for data uploads ([e60c794](https://github.com/rvben/shinyhub/commit/e60c7946faa43ad166d34d939a9cf056a1a06015))
- **data**: return ErrFileNotFound when app data dir is absent on Linux ([100ebb9](https://github.com/rvben/shinyhub/commit/100ebb963c963d2a30ea2824cfee72d45543bce2))
- **recovery**: validate process identity before re-adopting native apps ([208ef04](https://github.com/rvben/shinyhub/commit/208ef0494abe4970f1a3d587e1dc9c70a788e087))
- **security**: scrub server secrets from app-controlled code paths ([c7a015e](https://github.com/rvben/shinyhub/commit/c7a015edefddb09392e254d0a8afa95e9b1836b4))

### Performance

- **db**: WAL mode, busy_timeout, connection pool, latency logging ([2c9d10c](https://github.com/rvben/shinyhub/commit/2c9d10c5d71e1b8760704e441bc631a3ae2dc509))

## [0.5.0](https://github.com/rvben/shinyhub/compare/v0.4.1...v0.5.0) - 2026-05-14

### Added

- **ui**: show manifest summary in deploy modal ([b9ab11a](https://github.com/rvben/shinyhub/commit/b9ab11acde83ecbfba64ae94da9bb3bd4fcbd308))
- **deploy**: surface manifest-apply summary in response and CLI output ([9492cde](https://github.com/rvben/shinyhub/commit/9492cdeab463139a50c0852d458e29bd9b3e7f54))
- **api**: apply manifest [app] and [[schedule]] in two phases during deploy ([cf1fd33](https://github.com/rvben/shinyhub/commit/cf1fd332651e7ad454afc5d76a6193ccc501cc22))
- **db**: add atomic UpsertScheduleByName for declarative schedules ([f3a7902](https://github.com/rvben/shinyhub/commit/f3a7902429dd9534d41cb721a609575da5f63456))
- **deploy**: parse and validate [app] + [[schedule]] in shinyhub.toml ([dae1287](https://github.com/rvben/shinyhub/commit/dae12873cc4899c2a8864147d66ef9d0c3339f7f))
- **cli**: honor .shinyhubignore (or .gitignore fallback) when bundling ([94db539](https://github.com/rvben/shinyhub/commit/94db539d3c2da9d30fb143890f27878b1251ada1))

### Fixed

- **api**: pre-validate manifest before tearing down running pool ([deb224b](https://github.com/rvben/shinyhub/commit/deb224bf1821e35ee686a1d3b9cb43fe0230d332))
- **db**: make UpsertScheduleByName race-free via INSERT ON CONFLICT ([bc7ef6c](https://github.com/rvben/shinyhub/commit/bc7ef6c88d845e9fa466b2f60ca79f1e5b20495f))
- **cli**: prune ignored directories when no negation patterns present ([a593ebc](https://github.com/rvben/shinyhub/commit/a593ebc91ad293a4758cc39244b936e4d431bc0c))

## [0.4.1](https://github.com/rvben/shinyhub/compare/v0.4.0...v0.4.1) - 2026-05-13

### Added

- **shinyhub**: wire tracing into startup and document it ([72480c5](https://github.com/rvben/shinyhub/commit/72480c5ea1e47514a6bbd72e2d471f52fbc3f4f2))
- **ui**: add Traces tab to app detail ([6eafe21](https://github.com/rvben/shinyhub/commit/6eafe21f56a1c98af62aa650caad783f000b175b))
- **api**: expose GET /api/apps/:slug/traces ([3b4f84f](https://github.com/rvben/shinyhub/commit/3b4f84fae5f3083daa539163d60b6975b887cdf1))
- **proxy**: propagate W3C traceparent and record spans ([b1d9b32](https://github.com/rvben/shinyhub/commit/b1d9b32a23cf822777018f3179af48fbd252f340))
- **process**: inject platform default env per replica ([0e36b9f](https://github.com/rvben/shinyhub/commit/0e36b9fd0fb96300f119643e626dfa4bd73733e0))
- **tracing**: add W3C propagation, ring buffer, and OTEL env helper ([271d944](https://github.com/rvben/shinyhub/commit/271d944cf495f160232f34fca5e619def37ad046))
- **config**: add tracing block ([3ff9fca](https://github.com/rvben/shinyhub/commit/3ff9fcaebcaa9539362f4bf251045d813458e03e))
- **auth**: accept opaque SHINYHUB_DEPLOY_TOKEN values ([997721f](https://github.com/rvben/shinyhub/commit/997721f2ee4ffaf22ca5b901d0ba980da2059f4b))

## [0.4.0](https://github.com/rvben/shinyhub/compare/v0.3.3...v0.4.0) - 2026-05-13

### Added

- **cmd**: wire SHINYHUB_DEPLOY_TOKEN into the API server at startup ([c11e04a](https://github.com/rvben/shinyhub/commit/c11e04a97d5ff2a9bd1946c0c7385e79e51ee7f2))
- **api**: refuse role/password/delete on system users (e.g. __deploy__) ([a2783e8](https://github.com/rvben/shinyhub/commit/a2783e84aa688ddb446342fb427aeeb789886bd3))
- **api**: refuse persistent token creation and DB-key auth for system users ([137dab8](https://github.com/rvben/shinyhub/commit/137dab8b9a90118bcff1a5a6c16dc6c3ca37c960))
- **api**: pre-shared deploy token shadows api_keys in keyLookup ([74d52df](https://github.com/rvben/shinyhub/commit/74d52df744f899ce02af4e067fd26c0b4f2aa545))
- **db**: UpsertSystemUser and IsSystemUser for synthetic accounts ([11b5945](https://github.com/rvben/shinyhub/commit/11b59455c9d7c73347eabc50af89975d5f0885cc))
- **auth**: DeployToken type for env-sourced pre-shared credentials ([4f78412](https://github.com/rvben/shinyhub/commit/4f78412ad80df5c5ead9715900bdf5f5be656681))
- **config**: SHINYHUB_DEPLOY_TOKEN and SHINYHUB_DEPLOY_TOKEN_ROLE env vars ([60db7a0](https://github.com/rvben/shinyhub/commit/60db7a06665b42670e3d155dc6d11b71cca38e72))

### Fixed

- **api**: refresh session cookie on /me only when auth came from the cookie ([e0bea4f](https://github.com/rvben/shinyhub/commit/e0bea4f953939195f7d687ee66c9737ae0d69ef7))

## [0.3.3](https://github.com/rvben/shinyhub/compare/v0.3.2...v0.3.3) - 2026-05-13

### Added

- **deploy**: post-deploy hooks via shinyhub.toml ([e470f9f](https://github.com/rvben/shinyhub/commit/e470f9f68224e24a50301bcec9ee8367b74e4bda))
- **cli**: env apply for declarative dotenv sync ([60421ea](https://github.com/rvben/shinyhub/commit/60421ea4f5fb0e31927a867a8e90ae607c1dc55d))
- **proxy**: WS-readiness probe at /app/<slug>/.shinyhub/ready ([39eea66](https://github.com/rvben/shinyhub/commit/39eea6649a303eff3186e67e7999d446f83c28db))
- **api,cli**: apps logs --tail and --no-follow for one-shot fetches ([29fc83c](https://github.com/rvben/shinyhub/commit/29fc83c03724a7229c634db9c35f000d7fb4b2e7))

### Fixed

- **jobs**: normalize app data dir to absolute path ([21031f4](https://github.com/rvben/shinyhub/commit/21031f4a32d12b97e8a964b8dbc6535470f6deec))

## [0.3.2](https://github.com/rvben/shinyhub/compare/v0.3.1...v0.3.2) - 2026-05-12

### Added

- **tokens**: add --format json to tokens create and --name to tokens revoke ([10848f3](https://github.com/rvben/shinyhub/commit/10848f3b5d3ee7ab103648ed9bf769544c6bfdab))
- **schedule**: add --if-not-exists to schedule add; return 409 on duplicate name ([0d97163](https://github.com/rvben/shinyhub/commit/0d97163e7e5f28b49351f72054398ef8b28d7723))
- **config**: configurable default app visibility ([882059b](https://github.com/rvben/shinyhub/commit/882059bd47ed505de6ca14c770c969f6625db864))
- **deploy**: tail app logs to stderr when --wait times out or app crashes ([4b65bcc](https://github.com/rvben/shinyhub/commit/4b65bccda6b09577e0482d2e5947961c16d8f6ff))

## [0.3.1](https://github.com/rvben/shinyhub/compare/v0.3.0...v0.3.1) - 2026-05-12

### Fixed

- **process,jobs**: inject SHINYHUB_APP_DATA in native runtime for Start and RunOnce ([f309f93](https://github.com/rvben/shinyhub/commit/f309f93fd1ae3fff334f8fdd506ea833276eba3f))
- **process**: normalize app data root to absolute path ([7808879](https://github.com/rvben/shinyhub/commit/7808879781d0831f7a17cf0e839427652426b5b9))

## [0.3.0](https://github.com/rvben/shinyhub/compare/v0.2.6...v0.3.0) - 2026-04-27

### Added

- **cli**: prompt for missing password and username on login ([f3071d5](https://github.com/rvben/shinyhub/commit/f3071d5989a6187fa7132906db454ac5800d1265))
- **ui**: polish copy + identity — dark loading page, server host, snippet on new user ([daeb975](https://github.com/rvben/shinyhub/commit/daeb9756f1ace64fed025dac8dccc02e745a0eb5))
- **cli**: add logout, apps start/show, --config override, deploy guard ([480b75b](https://github.com/rvben/shinyhub/commit/480b75bca0f38074b6a8fa0551964a95e41c8a46))
- **ui**: copy-to-clipboard for app slugs and usernames ([55b50f6](https://github.com/rvben/shinyhub/commit/55b50f61ed668ce83f95a4d1327037b36b2f53cf))
- **ui**: client-side search and sort for apps grid ([92ab3c8](https://github.com/rvben/shinyhub/commit/92ab3c81086afcf68cde7b4e13a7c70ae44f1161))
- **ui**: serve SPA shell for /login route ([c5cdec3](https://github.com/rvben/shinyhub/commit/c5cdec3a5610670ab9dbf7f953b0f6be2b9800da))
- **cli**: --json output flag for list commands ([e6e2522](https://github.com/rvben/shinyhub/commit/e6e2522290c99ef321c1d1c2ca98f9df4707973e))
- **cli**: add apps delete, apps stop, apps deployments, tokens list and revoke ([35d843a](https://github.com/rvben/shinyhub/commit/35d843a9ebdf6a52cbd652e71d3431b6f47514ec))
- **cli**: implement deploy --wait with status polling and clean deploy output ([1a5df97](https://github.com/rvben/shinyhub/commit/1a5df97e8474dae8b87d5dd08117ad24f8a6e790))

### Fixed

- **auth,access**: centralise X-Forwarded-* trust gate via proxytrust package ([6a9286b](https://github.com/rvben/shinyhub/commit/6a9286b53f35150375b08e8635c160574cc464c8))
- **api,access**: respect proxied host in handoff CSRF check; styled error page for embedded-auth-header browsers ([d94a2f1](https://github.com/rvben/shinyhub/commit/d94a2f1c841d828be734d5d89fb4591c5ceb49eb))
- **access**: harden /app/* — ignore embedded-app Authorization, hand off 403 server-side ([6bd7f15](https://github.com/rvben/shinyhub/commit/6bd7f151f67c8edd150e357bdef5f59a34da2a12))
- **api,access**: make CreateDeployment authoritative; gate by deployments row ([b4f8255](https://github.com/rvben/shinyhub/commit/b4f8255d6fcd4049576748a1dadad6a1727714fa))
- **jobs**: honor cancellation when slot.lock select races with release ([fc0b86f](https://github.com/rvben/shinyhub/commit/fc0b86f46faa343869adbfd0b1487340605b06a8))
- **api**: keep deploy/restart/rollback 200 when post-orchestration bookkeeping fails ([7d52b10](https://github.com/rvben/shinyhub/commit/7d52b1029352066ead6d8631063f2dc9868330e0))
- **ui**: route 401 to login flow for deployments tab and access toggle ([97fba86](https://github.com/rvben/shinyhub/commit/97fba864839071309f6c37b5465ecdb877566ef7))
- **cli**: validate auto-derived slug locally before any network call ([02c2eb2](https://github.com/rvben/shinyhub/commit/02c2eb2d27fcb3de5c4e58c8920222f62206518b))
- **access**: re-resolve JWT user from DB so role demotions take effect ([813e9d5](https://github.com/rvben/shinyhub/commit/813e9d5b5c97c71dad7325d382175ffb57961877))
- **cli**: warn when env vars override the just-removed credentials ([886f355](https://github.com/rvben/shinyhub/commit/886f355a4623d05115d3e8eb1421eaa8fcf9321c))
- **ui**: scope pendingDeploy intent to the originating tab ([36b275c](https://github.com/rvben/shinyhub/commit/36b275c3a17fe6d11dc60bb8c5e80cd255ca85c9))
- **ui**: keep user signed in when logout POST is rejected by server ([b8ac030](https://github.com/rvben/shinyhub/commit/b8ac030a4c7f7ee0a6f7463a4317be12166ed60e))
- **ui**: bind ?logout=1 to a same-tab marker to block GET-driven logout ([8941a1c](https://github.com/rvben/shinyhub/commit/8941a1c74ee01ac4e0dd1c824b977bc06c85fad6))
- **ui**: pair ?logout=1 with /app/ next= and short-circuit auth re-check ([1a5f917](https://github.com/rvben/shinyhub/commit/1a5f917042c9e83906f001082a7034bf57003132))
- **cli**: refuse non-interactive apps delete without --yes ([06babbb](https://github.com/rvben/shinyhub/commit/06babbb895f3a419cfd775c92c86396b3b3a7b0f))
- **ui**: clear wrong session on /?logout=1 before bootstrapping SPA ([e9501ef](https://github.com/rvben/shinyhub/commit/e9501ef83c2eb9ac748383ba0899ac75e634576a))
- **access**: drop app name from denied page and route 403 through logout ([50e0bba](https://github.com/rvben/shinyhub/commit/50e0bba78137dc42485b5a26f04db6e59a9265fe))
- **proxy**: distinguish slug-not-found from slug-lookup-error ([52a3018](https://github.com/rvben/shinyhub/commit/52a301826985de69ba3f31dc175f64d935f7beba))
- **cli**: surface non-2xx HTTP status from pollAppStatus ([200b2ef](https://github.com/rvben/shinyhub/commit/200b2ef5ec8d10491e1029e37b79fffa0c314b2e))
- **ui**: surface deployments errors instead of masking 404 as empty ([e27fe1d](https://github.com/rvben/shinyhub/commit/e27fe1d70fb1b135f7ecb3176d58dec731345d9f))
- address codex round-3 review findings ([b75883b](https://github.com/rvben/shinyhub/commit/b75883b9e9ac5acac4511edc221050b2e1daf0bb))
- address codex review follow-ups ([2ee72d1](https://github.com/rvben/shinyhub/commit/2ee72d162f6e1ebacdc1abde828b128902aa84d3))
- address codex review findings ([7adcee3](https://github.com/rvben/shinyhub/commit/7adcee33f4d9d9c2d2c6b584de550c3a2148f66f))
- **ui**: SPA bleed-through, overview URL, never-deployed status, audit pagination ([40cff5e](https://github.com/rvben/shinyhub/commit/40cff5e4a521b92189c74fdce070e7b361cc5384))
- **api**: finalize deployment status, 404 unknown slugs, surface CLI errors ([691d5c1](https://github.com/rvben/shinyhub/commit/691d5c16bd05a93b1aa7245ebba994947b9059c6))
- **build**: drop deleted shiny binary, add make clean, fix air dev command ([cee605e](https://github.com/rvben/shinyhub/commit/cee605ed19c4f1130bebc3f3578734e948b70211))
- **ui**: distinguish empty deployments from API errors in history tab ([3b1f16a](https://github.com/rvben/shinyhub/commit/3b1f16abc6bca5cbba50f98c724418a8e640a8b9))
- **ui**: toast feedback for access visibility change and member grant ([da0c70d](https://github.com/rvben/shinyhub/commit/da0c70d376a82d1856dc14a3c85bdb1607f0106f))
- **access**: use shinyhub binary name in never-deployed snippet ([ede5a83](https://github.com/rvben/shinyhub/commit/ede5a832f2b813ebabcf62e07d12c8fc26afac73))
- **cli**: verify token with server round-trip on login --token ([e881076](https://github.com/rvben/shinyhub/commit/e88107669df278fe4931503d0f0e8d592fe2db01))
- **cli**: validate slug locally before making a network call on deploy ([fb98e1d](https://github.com/rvben/shinyhub/commit/fb98e1ddfdffa458effea7dda6bc2acbdc67d644))
- **cli**: treat -1 as unset sentinel for max-sessions-per-replica, reject invalid negative hibernate-timeout ([f9c93ab](https://github.com/rvben/shinyhub/commit/f9c93aba821e4ccf606e849b9bb44b5aa8589527))
- **cli**: exit non-zero when apps logs receives a server error ([df1ed8c](https://github.com/rvben/shinyhub/commit/df1ed8cf157c6418a492740fc076e756d3af1c98))

## [0.2.6](https://github.com/rvben/shinyhub/compare/v0.2.5...v0.2.6) - 2026-04-25

### Added

- **ui**: seed Overview Replicas panel from replicas_status ([be93f2f](https://github.com/rvben/shinyhub/commit/be93f2f3c0fc0328ed9cbdda0f562fe767e72329))
- **ui**: confirm before replica-count change drops sessions ([ea8a5ab](https://github.com/rvben/shinyhub/commit/ea8a5abb5c80e7f0ed3d48271cd73566fac0892f))

## [0.2.5](https://github.com/rvben/shinyhub/compare/v0.2.4...v0.2.5) - 2026-04-24

### Added

- **ui**: show admission ceiling live in Scaling fieldset ([daf5d4b](https://github.com/rvben/shinyhub/commit/daf5d4b0ad74796101937cfc58b7849134612d53))
- **ui**: add Scaling fieldset to Configuration tab ([41a4698](https://github.com/rvben/shinyhub/commit/41a46988b1c1c17280c7415f4abdbdf3e84ec285))
- **cli**: add --replicas and --max-sessions-per-replica to apps set ([54d0241](https://github.com/rvben/shinyhub/commit/54d0241945b48bbdbaed1fc4023964808ca61077))
- **ui**: show per-replica sessions badge in Overview tab ([9f97c79](https://github.com/rvben/shinyhub/commit/9f97c7950d00699856fb62e90fbac4c807739fbc))
- **api**: fan out metrics per replica and expose max_sessions_per_replica ([b3e155d](https://github.com/rvben/shinyhub/commit/b3e155d4cc88e8c89d69f059a28ac0bf77878100))
- **deploy**: thread session cap through deploy/lifecycle/recovery ([8a7d01a](https://github.com/rvben/shinyhub/commit/8a7d01a2e771c6567abdc02b54cfb80e6a95a4d0))
- **proxy**: shed 503 when all replicas saturated ([3333d67](https://github.com/rvben/shinyhub/commit/3333d67df4f16993cff18327a1f6698490752e3a))
- **config**: add runtime default for per-replica session cap ([aa3fa91](https://github.com/rvben/shinyhub/commit/aa3fa9134aa052bb81ba7f11ee6054ed1dd581c2))
- **db**: add migration 012 for max_sessions_per_replica column ([e5417ea](https://github.com/rvben/shinyhub/commit/e5417ea073c86d461d009224ec31a56ac1d90ae9))
- **ui**: focus + aria-current on navigation ([1da9b28](https://github.com/rvben/shinyhub/commit/1da9b289c5d583e507de6edc8208abef04811619))
- **ui**: simplify app grid card to two actions + kebab ([82cce3f](https://github.com/rvben/shinyhub/commit/82cce3f5f5ebc68296c822e89437044600bf5124))
- **ui**: migrate configuration/data/access panels into detail page ([34d7d58](https://github.com/rvben/shinyhub/commit/34d7d587be42aa58c802f0d0ffa67fd443a88499))
- **ui**: move deployment history into Deployments tab ([5ecebad](https://github.com/rvben/shinyhub/commit/5ecebad9ff122c9a6f0e7ddc6bd5f6d9112802de))
- **ui**: add Logs tab to app detail page ([25acb87](https://github.com/rvben/shinyhub/commit/25acb87eb3ec2cce566f7a6a0ce1a39897f43212))
- **ui**: add app detail page with overview tab ([9ae4f74](https://github.com/rvben/shinyhub/commit/9ae4f740512557d4b6ffbd60828f8ef9943944ef))
- **ui**: route /users and /audit-log via client router ([4613919](https://github.com/rvben/shinyhub/commit/4613919bd519a68d2397200328703847270d4e7d))
- **ui**: add metrics polling controller ([6e5a514](https://github.com/rvben/shinyhub/commit/6e5a5149a06ef37bc29f93ddd7bc436e26d571aa))
- **ui**: add app detail page skeleton ([b35d846](https://github.com/rvben/shinyhub/commit/b35d8469e6cb39b3687caeb9d4913dccdd7c70e0))
- **ui**: add client-side router module ([13a4ee4](https://github.com/rvben/shinyhub/commit/13a4ee4c1bfdf6906387414e830d477c07f725ad))
- **ui**: route SPA paths to index.html ([06ad56c](https://github.com/rvben/shinyhub/commit/06ad56c8dc3c5528cdce458bc5571f5060ad942b))
- **ui**: add SPA fallback handler for routed UI paths ([a3d9375](https://github.com/rvben/shinyhub/commit/a3d93755ebfb76cc79b7d02e2ec384585ad51bd3))
- **ui**: add CLI snippet to Deploy modal and unify modal button styles ([0697aef](https://github.com/rvben/shinyhub/commit/0697aefe40318878efe870dd9a5f29424dc2dd22))

### Fixed

- **ui**: unwrap app-detail response body ([45de18d](https://github.com/rvben/shinyhub/commit/45de18d09fce5cff2297fe1ae9f526757224b76d))
- **ui**: hide management-only detail tabs from viewers ([d51fa64](https://github.com/rvben/shinyhub/commit/d51fa64307cd0d54fbfeb4e43e8efeda75f5fa76))
- **ui**: await initial router mount and refresh metrics on loadApps ([2795563](https://github.com/rvben/shinyhub/commit/27955636a6fc029cdcc50990202c09d2ebaf38d4))
- **ui**: improve a11y of app detail page tabs and kebab menu ([f494b2c](https://github.com/rvben/shinyhub/commit/f494b2c57b8e48d84e87ca78042e0497271b49bb))
- **ui**: guard router mount against races and unregistered-path recursion ([c85640f](https://github.com/rvben/shinyhub/commit/c85640f23e5005af0a224081646f18fe1efeb3c2))
- **ui**: apply dark theme to modal textareas, number inputs, and checkboxes ([155c840](https://github.com/rvben/shinyhub/commit/155c84052724f0f748c9a018fb0f3c6274ee6795))

## [0.2.4](https://github.com/rvben/shinyhub/compare/v0.2.3...v0.2.4) - 2026-04-22

### Breaking Changes

- **cmd**: merge shiny CLI into shinyhub binary with subcommands ([e960e44](https://github.com/rvben/shinyhub/commit/e960e44d7cf80917a2a0946ba084d86fac5fddb9))

### Added

- **packaging**: publish sdist as stub for unsupported platforms ([2face79](https://github.com/rvben/shinyhub/commit/2face79b5463083d3e60b1b5d5ef1cfb0e1a121d))
- **packaging**: add PyPI wheel skeleton ([c1d5e9b](https://github.com/rvben/shinyhub/commit/c1d5e9bfff6f5133f4834958095514439d36be9d))
- **cmd**: merge shiny CLI into shinyhub binary with subcommands ([e960e44](https://github.com/rvben/shinyhub/commit/e960e44d7cf80917a2a0946ba084d86fac5fddb9))

### Fixed

- address code review feedback on pypi branch ([64c79bc](https://github.com/rvben/shinyhub/commit/64c79bc45c5b2f60fe1037e3efc173e9e56caa34))
- **docker**: include serve subcommand in ENTRYPOINT ([b08bb1a](https://github.com/rvben/shinyhub/commit/b08bb1a1eda541bb453dd2cf00b3e4f66f7ad74d))

## [0.2.3](https://github.com/rvben/shinyhub/compare/v0.2.2...v0.2.3) - 2026-04-22

### Added

- **ui**: expose per-app hibernate timeout in Settings modal ([3a4c07d](https://github.com/rvben/shinyhub/commit/3a4c07d1ca452dbff57ea179741e31b11fc7842c))
- **proxy**: forward real client identity and log every proxied request ([e706d66](https://github.com/rvben/shinyhub/commit/e706d666e008a99f0e8b438c75259eb5e1cdce12))

### Fixed

- **ui**: redirect to login when metrics polling returns 401 ([ae41785](https://github.com/rvben/shinyhub/commit/ae417854772776d9a63ad038d3acb1b00bc95a00))
- **api**: skip per-slug data lock when quota is disabled ([4fef6ac](https://github.com/rvben/shinyhub/commit/4fef6ac98929be10d84f2ad7f1aa0aa484b1107e))
- **jobs**: release queue slot when a queued run becomes active ([7eb7e43](https://github.com/rvben/shinyhub/commit/7eb7e43292126250eea416f5fb85692a9a5b589b))
- **jobs**: preserve admission order for back-to-back queue-policy runs ([1c0c3bc](https://github.com/rvben/shinyhub/commit/1c0c3bcdc65da0f611c677df2d7e2102df5df23c))
- **jobs**: apply per-run timeout at execute time and free queue slot on cancel ([7f04dbd](https://github.com/rvben/shinyhub/commit/7f04dbddb81f81baff35b1efae8b8ef439af2146))
- **jobs**: register queued run cancel before mu.Lock so Cancel works ([27412e8](https://github.com/rvben/shinyhub/commit/27412e8c5fc5789d1b5bd15c2fbb7b08ced7f231))
- **server**: exempt data PUT route from 30s API timeout ([f22e08d](https://github.com/rvben/shinyhub/commit/f22e08da6996234c0b67bb461dab94008bbcf9d6))
- **proxy**: hold route lock across activeConns bump ([e660d3d](https://github.com/rvben/shinyhub/commit/e660d3d293bebbdd97c1d229ed7f1afa294db64d))
- **jobs**: grow queue overlap semaphore to capacity 2 so it actually queues ([d7613a4](https://github.com/rvben/shinyhub/commit/d7613a4e5349f71ae46489fc1de8c103f0d07729))
- **api**: serialize per-slug quota check and write in handleDataPut ([93bcc72](https://github.com/rvben/shinyhub/commit/93bcc725d35922035e8f64aa96b3830f0525da41))
- **auth**: re-resolve JWT user against DB on every request ([440ef6d](https://github.com/rvben/shinyhub/commit/440ef6d02260d27370ab0f996ae5efc1f91223e7))
- **api**: remove multipart spill files after bundle upload ([cf23606](https://github.com/rvben/shinyhub/commit/cf23606b27fd3cc6bf7b83afda8e495bc77db7bb))
- **api**: clean up orphan bundle dirs and zips on failed deploy ([3a3c02a](https://github.com/rvben/shinyhub/commit/3a3c02adf1b4aead9411ec9193e04f53ab527ae8))
- **api**: validate rollback target bundle before stopping live app ([c0d955e](https://github.com/rvben/shinyhub/commit/c0d955e6af8d099255cef2c4c7f15d26632b9ec2))
- **process**: poll PID for adopted native processes in Wait ([28a93f6](https://github.com/rvben/shinyhub/commit/28a93f6ba26ab9cd21190978c5081879793f2c7d))

## [0.2.2](https://github.com/rvben/shinyhub/compare/v0.2.1...v0.2.2) - 2026-04-22

### Added

- **runtime**: default Docker network mode to bridge with loopback port mapping ([20d3a1c](https://github.com/rvben/shinyhub/commit/20d3a1ca61e0613509b0cb2e09bacf1e004f0106))

### Fixed

- **lifecycle**: close hibernation race with CAS-style BeginHibernate ([fc8f870](https://github.com/rvben/shinyhub/commit/fc8f8706a8f6adce071631f318dd2a73b2468066))
- **deploy**: probe each candidate port for bindability before assigning it ([024d38c](https://github.com/rvben/shinyhub/commit/024d38cef65abefe3c6ddf9d30eebcef2097a32f))
- **deploy**: skip host-side dep install when runtime is containerized ([ca96264](https://github.com/rvben/shinyhub/commit/ca962647e3f7360fd8507f3eac886b69d1616968))
- **config**: reject unknown runtime.mode values at startup ([a94ce46](https://github.com/rvben/shinyhub/commit/a94ce46879eca7051104bce95f6f85712d3ae81d))
- **api**: serialize per-app deploy/restart/rollback with a per-slug mutex ([b2826d8](https://github.com/rvben/shinyhub/commit/b2826d83528937ef10bede2da22b8ab3fe49a339))
- **data**: reject symlink traversal in Put and Delete ([601a0f9](https://github.com/rvben/shinyhub/commit/601a0f9ba9150102dcce67e9e64c6e331aef24b9))
- **auth**: bind OAuth state nonce to the originating browser ([4e8fed1](https://github.com/rvben/shinyhub/commit/4e8fed1d2f1f296bfe35d58735324e3deb03f387))
- **auth**: default OAuth JIT users to viewer role ([2ab7ae0](https://github.com/rvben/shinyhub/commit/2ab7ae0c1e7d445e2bc9ee139e76adb42187ba04))
- **api**: require explicit access on source app to grant shared-data mount ([11e3dee](https://github.com/rvben/shinyhub/commit/11e3dee4787b4de45ea2ea7fd0026c6cea05b90f))
- **api**: require manage rights to read schedule run logs ([115ba80](https://github.com/rvben/shinyhub/commit/115ba809d7e8d54e94cbcd53852a59e1c7491630))

## [0.2.1](https://github.com/rvben/shinyhub/compare/v0.2.0...v0.2.1) - 2026-04-21

### Added

- **cli**: add 'shiny schedule' and 'shiny share' subcommands ([e6dd1a5](https://github.com/rvben/shinyhub/commit/e6dd1a548484e1961a899542bbddf27e3d84f683))
- **ui**: add Schedules and Shared-data sub-tabs to Settings modal ([59d63c6](https://github.com/rvben/shinyhub/commit/59d63c6e864509b862c55535458e190631b3a70b))
- **process**: resolve and apply shared mounts during Manager.Start ([50d2f3d](https://github.com/rvben/shinyhub/commit/50d2f3d59986ea0b19427f0bfc5e61eff39a2255))
- **api**: stream per-run schedule logs over SSE ([8117182](https://github.com/rvben/shinyhub/commit/81171821f3b76bc26b993e15c6eb8974470c4631))
- **api**: add schedule CRUD, manual run, history, and shared-data endpoints ([dbc4530](https://github.com/rvben/shinyhub/commit/dbc45309324f5a20a322c00bd7457a725357dd2e))
- **server**: construct jobs.Manager and scheduler at startup ([e30797f](https://github.com/rvben/shinyhub/commit/e30797f1729b3c81812682a8259b6030ba7ac9a0))
- **lifecycle**: add cron-driven Scheduler with missed-run catch-up ([bab13ca](https://github.com/rvben/shinyhub/commit/bab13ca02b30c9e7f7d0fd60d41d73509816516c))
- **jobs**: add Manager that runs scheduled commands with overlap policies ([da3dd1d](https://github.com/rvben/shinyhub/commit/da3dd1d8fd9797a795d17dafde17a4efc35e5aea))
- **process**: add RunOnce + shared-mount support to native and docker runtimes ([ceaaa15](https://github.com/rvben/shinyhub/commit/ceaaa15f44bb71191212b3c391911758e9f705b0))
- **db**: add CRUD for schedules, schedule runs, and shared data mounts ([c3a013f](https://github.com/rvben/shinyhub/commit/c3a013f710819ae90edb2e74b8d36e2586e867c8))
- **db**: add 011_schedules.sql for app_schedules, schedule_runs, app_shared_data ([05f5087](https://github.com/rvben/shinyhub/commit/05f5087e2e45d0f7d9d810aaed965d90991d99fe))
- **api**: PATCH /api/apps/:slug accepts replicas, enforces max_replicas ([18314e1](https://github.com/rvben/shinyhub/commit/18314e1546285655e67e911842917b3510bf6b5f))
- **api**: expose replicas and per-replica status on GET /api/apps/:slug ([3db75f6](https://github.com/rvben/shinyhub/commit/3db75f614a5df0ce1c43f4235bd9a34db3079aaa))
- **config**: runtime.default_replicas and runtime.max_replicas ([427fd49](https://github.com/rvben/shinyhub/commit/427fd4909bc165a9f8edd17be92aebb7f9a0c3ad))
- **lifecycle**: pool-aware docker recovery via replica_index label ([933c48b](https://github.com/rvben/shinyhub/commit/933c48b024ac1bb496e7d03c1a2844baee0b56bf))
- **lifecycle**: pool-aware native process recovery ([03df897](https://github.com/rvben/shinyhub/commit/03df897111fb6c5f141da891690e28daef824bba))
- **lifecycle**: hibernate drains the pool; OnMiss wakes all replicas ([8f8486d](https://github.com/rvben/shinyhub/commit/8f8486d47d95659d70824beff6ed464692ceedd1))
- **lifecycle**: per-replica crash tracking with pool-aware degraded state ([d613c12](https://github.com/rvben/shinyhub/commit/d613c12d20c3ee36cf780ca9174ba86b9606f0c3))
- **server**: sweep stale data upload tempfiles on startup ([a3281d9](https://github.com/rvben/shinyhub/commit/a3281d9327844244a6131567c4f21a71f16f5dcd))
- **ui**: add Data tab to Settings modal with upload, list, and delete ([e2331ba](https://github.com/rvben/shinyhub/commit/e2331ba752d83182ea9332938aa9e7b54ec3a82e))
- **cli**: add 'shiny data push|ls|rm' subcommands ([5cde490](https://github.com/rvben/shinyhub/commit/5cde4908dd98db168aae825101190ae326644cef))
- **api**: GET /apps/:slug/data with quota envelope and stricter auth ([21063ac](https://github.com/rvben/shinyhub/commit/21063ac476b5391afe9c0913423e7c6cb402dd74))
- **api**: DELETE /apps/:slug/data/*path ([da1caf2](https://github.com/rvben/shinyhub/commit/da1caf240b473177cd1f82014fd6b17e5dd34462))
- **api**: PUT /apps/:slug/data/*path with quota and audit ([1171d89](https://github.com/rvben/shinyhub/commit/1171d897694a02d75cc546d973c02f0ad3bd42ee))
- **db**: add data.push and data.delete audit action constants ([0f45ca9](https://github.com/rvben/shinyhub/commit/0f45ca9ab55a418f7b66ab97203a2509c49737c3))
- **api**: add requireExplicitAppAccess helper for stricter data-API auth ([7278e4f](https://github.com/rvben/shinyhub/commit/7278e4f299173c25edfb2d1cbe9fcb0eb165ce4b))
- **process**: bind app data dir into Docker container at /app-data and /app/data ([a1fec41](https://github.com/rvben/shinyhub/commit/a1fec416a2610fa6d5287ec1a2e148285d6e5794))
- **deploy**: add RunReplica for single-replica restart path ([870ca98](https://github.com/rvben/shinyhub/commit/870ca98eda551d001bdf6fbb545a7b1307fde89b))
- **deploy**: parallel pool boot returning PoolResult ([ad793de](https://github.com/rvben/shinyhub/commit/ad793ded5cb9559cc77d859fde58e33f71a8b076))
- **process**: inject SHINYHUB_APP_DATA and symlink bundle/data on native start ([89d9f1e](https://github.com/rvben/shinyhub/commit/89d9f1ebf3d56828dd495bf54ef9b75aee8e258e))
- **deploy**: include app data dir in per-app quota accounting ([5b1c51e](https://github.com/rvben/shinyhub/commit/5b1c51ebdf869e9be4d7ae17f7bf8841220a96a6))
- **data**: add List, Delete, DirSize, CleanupUploadTemp ([02307e3](https://github.com/rvben/shinyhub/commit/02307e32dc3dd3e5ebef6d1420a6987199b07363))
- **data**: add atomic Put with SHA-256 and temp-file rename ([013ffc0](https://github.com/rvben/shinyhub/commit/013ffc0927e01ebb30e06a5186f5ae4c62b3b918))
- **data**: overwrite-aware ProjectedSize and QuotaCheck ([1419a91](https://github.com/rvben/shinyhub/commit/1419a91eb0a1ebe5a72f1fe77c0cb10742e344b6))
- **data**: add path sanitization with reserved-prefix enforcement ([463c535](https://github.com/rvben/shinyhub/commit/463c535f8b1c463a43b2cc51d2cb78ebc687b976))
- **ui**: drive deploy zipper from generated bundle rules ([28d96bc](https://github.com/rvben/shinyhub/commit/28d96bccd2dbfb125a0ca7e9a46a95a4aefcc4c6))
- **api**: enforce MaxBundleMB on deploy uploads ([3c47ee5](https://github.com/rvben/shinyhub/commit/3c47ee5522a9cab884941fa191a28c9d4542ce23))
- **api**: surface bundle rejections as 422 ([ce4a5ef](https://github.com/rvben/shinyhub/commit/ce4a5ef371d52628653a6e386cd8aafed3bc3531))
- **deploy**: enforce bundle filter rules on extraction ([336de47](https://github.com/rvben/shinyhub/commit/336de47e5a4a2a589a8fc80dabcc50fed6a5f380))
- **bundle**: emit bundle-rules.json for the JS zipper with golden test ([f8f91d5](https://github.com/rvben/shinyhub/commit/f8f91d5e34a0fd180c8e65828bf024966d2d2e9c))
- **bundle**: add shared filter rules with FilterDecision classifier ([df3b6c2](https://github.com/rvben/shinyhub/commit/df3b6c2bd757657c28beb87d1992db636663af85))
- **server**: create app-data dir on startup ([6fdfce3](https://github.com/rvben/shinyhub/commit/6fdfce3b78763c4aa529cd49a2f2d71899bbaac2))
- **proxy**: sticky cookie + least-connections routing with round-robin tie-break ([37788f0](https://github.com/rvben/shinyhub/commit/37788f0017cc0150a19e35b99f15409e5f8f650a))
- **api**: refuse app create when slug has lingering on-disk state ([4b10305](https://github.com/rvben/shinyhub/commit/4b1030586bf8d7ef6dff4f15413ee38718449b77))
- **proxy**: replace single-backend-per-slug with replica pool ([6cc21d8](https://github.com/rvben/shinyhub/commit/6cc21d825db7713b95fa17155f832aba244aacdf))
- **storage**: add slug-cleanup helpers RequireFreeSlug and OnAppDelete ([e1bd8cc](https://github.com/rvben/shinyhub/commit/e1bd8cca1c4838709106fd750e3dcfc063a0a994))
- **process**: pool-per-slug manager with StopReplica and parallel Stop ([e44185f](https://github.com/rvben/shinyhub/commit/e44185fc4ece099930962ea27913041441b11566))
- **process**: add replica Index field and pool storage to Manager ([38ec34b](https://github.com/rvben/shinyhub/commit/38ec34b9a030ea225ef4237639d71df8f51ac646))
- **config**: add app_data_dir and max_bundle_mb storage settings ([b9f9863](https://github.com/rvben/shinyhub/commit/b9f9863323abb0dd61e203afb2f0726beca51064))
- **db**: add Replica struct and UpsertReplica/ListReplicas/DeleteReplica ([0d6ec6d](https://github.com/rvben/shinyhub/commit/0d6ec6d02c9f2ab9caf2e3f4dd9f940681e57cc8))
- **db**: add replicas table and per-app replicas column ([0455f30](https://github.com/rvben/shinyhub/commit/0455f30e321fc49c9736833da8e10a3a0713bfc7))

### Fixed

- **process**: pre-create shared mount targets host-side ([6416e34](https://github.com/rvben/shinyhub/commit/6416e3425c17f9a596de65b029a5690995be90c6))
- **jobs**: resolve bundle dir from latest deployment ([887a4f8](https://github.com/rvben/shinyhub/commit/887a4f8ebccc1f5d21c07208b2dbd45678fbf8df))
- **scheduler**: accept 5-field cron expressions ([cab65c7](https://github.com/rvben/shinyhub/commit/cab65c73f62cf5a2b854487a8cb836e417f0b5f9))
- **server**: await scheduler shutdown before exiting main ([cba53e0](https://github.com/rvben/shinyhub/commit/cba53e05fa7ea41783bc39fdabecf61c799833e3))
- **cli**: split decode/empty-list errors and add share command tests ([9752633](https://github.com/rvben/shinyhub/commit/97526330b4a7e3566560c70853a5e918b32bec7f))
- **cli**: add --follow flag to 'shiny schedule logs' ([1f1c42b](https://github.com/rvben/shinyhub/commit/1f1c42bb0e765433bbed4a525db4689c2b971498))
- **ui**: harden cron parser, theme schedule tab styles ([1e1536e](https://github.com/rvben/shinyhub/commit/1e1536e19a290b738d8e045ff718e871bc9deccc))
- **ui**: show next 5 cron fires in preview, not 3 ([7c7fe7d](https://github.com/rvben/shinyhub/commit/7c7fe7dc8761e05671798513c26481deb1600475))
- **api**: order ownership before LogPath check; add cross-app run tests ([037a7e3](https://github.com/rvben/shinyhub/commit/037a7e3fb300412f6ce4ea0542ca1ea38a9b2047))
- **api**: close schedule-run cancel IDOR; validate ID parsing ([707dd75](https://github.com/rvben/shinyhub/commit/707dd759bb381beb59e97e800e8faa9aeda1408b))
- **server**: cancel scheduler before exit on serve error; gofmt ([6749d54](https://github.com/rvben/shinyhub/commit/6749d54e223aeb406ed126b8107c9b059333bd2b))
- **jobs**: persist schedule run log_path; drop unused GetApp from Store iface ([0a9563e](https://github.com/rvben/shinyhub/commit/0a9563e2f1dbd2d7d513b4ba8b9748d1fd7577ff))
- **jobs**: align Cancel signature with spec ((runID) error) ([6846255](https://github.com/rvben/shinyhub/commit/6846255aca325f89822724b6e7b56457cd647597))
- **process**: propagate Docker container exit code via wait response ([3207cb9](https://github.com/rvben/shinyhub/commit/3207cb94781e7610eac0a53cf09253ce5bccda51))
- **db**: return ErrNotFound from FinishScheduleRun on missing run ([0baafd0](https://github.com/rvben/shinyhub/commit/0baafd0e01b0a0ef0d3b75750c6b92f1126f1e78))
- **api**: propagate default_replicas on create; clear replica rows on stop ([3e12991](https://github.com/rvben/shinyhub/commit/3e129919c35b10a3d6b98a96d8226622f0051468))
- **api**: guard handleGetApp DB errors; serialize concurrent redeployApp per slug ([2135356](https://github.com/rvben/shinyhub/commit/2135356fdb3718ede81066af4f86cb82f13f3aa1))
- **lifecycle**: mark nil-PID replicas crashed; memoize ListReplicas in docker recovery ([5a3404b](https://github.com/rvben/shinyhub/commit/5a3404b6d0e1e390dd56a9b13796a6a4e335330e))
- **lifecycle**: gate wake-success status on at least one replica; deterministic test waits ([d7af3d7](https://github.com/rvben/shinyhub/commit/d7af3d7bad4967100134302eaddea64a7eb7e8b4))
- **process**: make bundle/data symlink idempotent for restart and wake ([918a089](https://github.com/rvben/shinyhub/commit/918a089a8568ae42b7079caaa63ad5611067f16c))
- **ui**: normalize leading slash in JS bundle inspector ([db1fbf9](https://github.com/rvben/shinyhub/commit/db1fbf9311524224f3bc4fe08ba29cab6c4779e8))
- **cli**: stabilize bundle rejection summary order ([8540dc2](https://github.com/rvben/shinyhub/commit/8540dc2fc3ffcaacac51a328c5dff9e8025fe569))
- **deploy**: inspect bundle entries before mkdir to honor data-dir rejection ([e0a6199](https://github.com/rvben/shinyhub/commit/e0a6199c38ea24a1c22bda5dece9c8ef20f6e81a))
- **process**: platform env wins on duplicate keys with user env ([f07e2fb](https://github.com/rvben/shinyhub/commit/f07e2fb8b0d643b222accf0415193f2e3a021c19))
- **api**: clean up apps and data dirs after app deletion ([45fc354](https://github.com/rvben/shinyhub/commit/45fc354add6a75c4a2db28da964a6ff242629789))
- **config**: treat MaxBundleMB=0 as no cap, only normalize negatives ([55cc40b](https://github.com/rvben/shinyhub/commit/55cc40beeed86d4677f61aaec30e34f6929a1c7d))

## [0.2.0](https://github.com/rvben/shinyhub/compare/v0.1.0...v0.2.0) - 2026-04-20

### Added

- **ui**: add Environment tab to Settings modal for managing per-app env vars ([b791e01](https://github.com/rvben/shinyhub/commit/b791e018ab62f4b403b5db309ffee2133828b57f))
- **cli**: add shiny env set|ls|rm commands ([23eff29](https://github.com/rvben/shinyhub/commit/23eff291d1355efee52b374c64e249263dcb5b99))
- **api**: delete per-app env vars with audit and optional restart ([81e9519](https://github.com/rvben/shinyhub/commit/81e9519396dd7b76ece7fcd339f7e2ac2c36b473))
- **api**: upsert per-app env vars with crypto, caps, audit, and optional restart ([667fa72](https://github.com/rvben/shinyhub/commit/667fa726c7f56e8455dbc480bdf8459e30c158b9))
- **api**: list per-app env vars with secrets masked ([a51c131](https://github.com/rvben/shinyhub/commit/a51c1316454f9482786eb7cdcf24c799bc557a3d))
- **process**: wire per-app env resolver into process manager ([c0420f2](https://github.com/rvben/shinyhub/commit/c0420f20021662369b77b04ac770ac1c45fcc3b1))
- **secrets**: add AES-GCM encrypt/decrypt with HKDF-derived key ([d8e7c57](https://github.com/rvben/shinyhub/commit/d8e7c5740ef520d53f46fce7a304ff228f9bb818))
- **db**: add CRUD methods for app_env_vars ([54f8af7](https://github.com/rvben/shinyhub/commit/54f8af7d4f6fd434bddc1970b0b425c595b644dd))
- **db**: add app_env_vars table for per-app env vars and secrets ([54a355a](https://github.com/rvben/shinyhub/commit/54a355adf787d42757c164758cfd26f72c39c813))
- **ui**: add admin Users tab with create, delete, role change, and password reset ([51ad711](https://github.com/rvben/shinyhub/commit/51ad7117d63eeea2a966c39adb0440210583def5))
- **auth**: centralize global role validation and accept viewer ([af2c63b](https://github.com/rvben/shinyhub/commit/af2c63bd14c98f2f246782faa55e0bdb1aabf8d7))
- **ui**: add delete-app flow with typed-confirmation ([1064bc5](https://github.com/rvben/shinyhub/commit/1064bc5fde1995521c0b9ef014cbe3f2d326d43c))
- **ui**: guide first-run users from an empty dashboard ([0de06d1](https://github.com/rvben/shinyhub/commit/0de06d1076a571621d71884178b92087c3e1b942))
- **ui**: add first-run empty state for never-deployed apps ([ecd63e9](https://github.com/rvben/shinyhub/commit/ecd63e932839f158fa8f43633c7192171b6a8323))
- **deploy**: enforce per-app disk quota on deploy ([ad3053f](https://github.com/rvben/shinyhub/commit/ad3053fa3fe403c3d5601b3256cad64cc1c7e563))
- **config**: add storage.app_quota_mb for per-app disk cap ([125088d](https://github.com/rvben/shinyhub/commit/125088d38233478dfcf8a4b5da4c6551ac73482a))
- **deploy**: add DirSize helper and quota sentinel ([ffa8714](https://github.com/rvben/shinyhub/commit/ffa87142ca0b004770d71d30179286883449b853))
- **auth**: revoke JWTs on logout via jti denylist ([9f57e9f](https://github.com/rvben/shinyhub/commit/9f57e9fc2026a9295a8ae71c2924f512b610c77d))
- **deploy**: guard against zip-bomb bundles ([ed922af](https://github.com/rvben/shinyhub/commit/ed922af4f765b77784c152d79522316b8bd3aecb))
- **ui**: auto-suggest slug from display name + zip writer round-trip test ([39e4e38](https://github.com/rvben/shinyhub/commit/39e4e38f796f5c9f43a0568c8c2b10e0e8a74243))
- **proxy**: add JS-driven reload with retry cap to loading page ([7b6b9dd](https://github.com/rvben/shinyhub/commit/7b6b9ddad0227becf1d6e6b468204c65ee6816a9))
- **ui**: zip folders natively via CompressionStream with JSZip fallback ([baec492](https://github.com/rvben/shinyhub/commit/baec492adc26d2a6ab693fefad53f7559ad4579d))
- **ui**: upload bundle from browser with progress and log tail ([67b7e3b](https://github.com/rvben/shinyhub/commit/67b7e3b7f3dc6118372a9fb44f0b45791d0f887e))
- **ui**: zip dropped folders client-side with JSZip ([c7312b1](https://github.com/rvben/shinyhub/commit/c7312b16464e41ef5fe4a9e1984c7beb84e5dd52))
- **ui**: deploy modal scaffolding with zip pick and drop ([5e6de80](https://github.com/rvben/shinyhub/commit/5e6de80554c92f9440435f3df9f44a7112b4bc44))
- **ui**: create apps from browser with CLI hand-off ([06c10eb](https://github.com/rvben/shinyhub/commit/06c10ebb3ec43c97a0cc2b061d90c204f09e5af8))
- **ui**: surface can_create_apps capability in session state ([6fc7467](https://github.com/rvben/shinyhub/commit/6fc7467b8d63d6b824733a1f42f4245711f75f5c))
- **ui**: style new-app and deploy modals ([26c9841](https://github.com/rvben/shinyhub/commit/26c9841b50c17c03cc5963548cacce6719998b83))
- **ui**: scaffold deploy modal with dropzone and progress ([136b1f5](https://github.com/rvben/shinyhub/commit/136b1f58cbc1a9732c14c8a6b22706f651316ced))
- **ui**: scaffold new-app modal and toolbar button ([73fdb38](https://github.com/rvben/shinyhub/commit/73fdb3841edac86bae3e42adcc5e6a6fa7641bf6))
- **api**: expose can_create_apps in session payload ([74c104a](https://github.com/rvben/shinyhub/commit/74c104a3b0bfc558acde5664704d99d0ad7860e2))
- **ui**: redesign dashboard with Constellation theme ([c2b11c0](https://github.com/rvben/shinyhub/commit/c2b11c0ca06889621e31fe307221bda0bf3c3a5e))
- **dev**: add live-reload dev environment via air ([de751b8](https://github.com/rvben/shinyhub/commit/de751b8f2054f8456c48e1e556898416b03b9b6f))
- **docker**: add distroless Dockerfile ([5639e32](https://github.com/rvben/shinyhub/commit/5639e327de14f266caab084c5f93b33791816c62))
- **logging**: migrate to structured slog with text/json handlers ([9cab46c](https://github.com/rvben/shinyhub/commit/9cab46cc30ddec686b6acb233121d9aa06f6980f))
- **server**: add /readyz endpoint with DB ping and startup gate ([2b62c85](https://github.com/rvben/shinyhub/commit/2b62c85992b8096ccf6c2ef6902e1f50fccb0c61))
- **server**: graceful shutdown on SIGTERM/SIGINT ([b5dfddf](https://github.com/rvben/shinyhub/commit/b5dfddf19ff222acbbfa4d8fe9662321c445d520))
- **api**: add per-user rate limits on deploy, user-create, token-create ([1589591](https://github.com/rvben/shinyhub/commit/158959110daf001b3a004bb369cd62b0078b98ed))
- **auth**: add CSRF double-submit cookie middleware ([eb11cce](https://github.com/rvben/shinyhub/commit/eb11cce0d5246a351caea94fab3a164b3a8de63c))
- **server**: add HTTP server timeouts with SSE and upload exemptions ([54fe0b0](https://github.com/rvben/shinyhub/commit/54fe0b08b428fc75e9d9ac89c17ffd5442bcdc8b))
- **config**: reject placeholder and sub-32-char auth secrets ([f6e0e47](https://github.com/rvben/shinyhub/commit/f6e0e4725c4b402c147e9fdc178b4cb9ba5b2cb7))
- **ui**: add deployment history modal with targeted rollback ([67b8562](https://github.com/rvben/shinyhub/commit/67b856255ba723734eee74b9778d6f13e0bc3f0c))
- **ui**: add audit log tab with paginated event table ([ef8a830](https://github.com/rvben/shinyhub/commit/ef8a830479fc983f696437e9d57a906ef2de1461))
- **ui**: add showView helper, tab switching, and tab bar wiring ([7242837](https://github.com/rvben/shinyhub/commit/7242837a17545d3328700aebe2d9c7ec4a3e7e35))
- **ui**: add tab bar, audit table, deployment badges, and history modal styles ([ec8b9c4](https://github.com/rvben/shinyhub/commit/ec8b9c48ee0d1c87a93f59b965a5df7c60ac3628))
- **ui**: add tab bar, audit view, and history modal HTML ([6696399](https://github.com/rvben/shinyhub/commit/669639991f4d6c30d35dd0c1034fa75063bff147))
- **db**: add username to AuditEvent via LEFT JOIN on users ([903bab4](https://github.com/rvben/shinyhub/commit/903bab4263edfcf2cf97967c024cb58c18f36589))
- **metrics**: route stats through Runtime.Stats for Docker containers ([3619a2f](https://github.com/rvben/shinyhub/commit/3619a2f76716e0c9b8c99088f7bc43d0512052be))
- **lifecycle**: add Docker container recovery path via label query ([8f91f98](https://github.com/rvben/shinyhub/commit/8f91f98beda728c0103163e81f2408b6cd0ed592))
- wire resource limits through deploy params and runtime selection at startup ([576df71](https://github.com/rvben/shinyhub/commit/576df71847ebe44520e74ff75760d28d5c097f95))
- **api**: add memory_limit_mb and cpu_quota_percent to app PATCH and GET ([b6b407d](https://github.com/rvben/shinyhub/commit/b6b407d7b1f36deecf31d4bb4faea04c54e33352))
- **db**: add memory_limit_mb and cpu_quota_percent columns to apps table ([decd6d1](https://github.com/rvben/shinyhub/commit/decd6d1f3c9cb964607542c336f9eea68546fedd))
- **config**: add RuntimeConfig with docker mode, socket, images, and resource defaults ([9ac8a6e](https://github.com/rvben/shinyhub/commit/9ac8a6e20cca94aecdbeae659aeace3ce27797c9))
- **process**: add DockerRuntime using Docker Engine API ([522f74f](https://github.com/rvben/shinyhub/commit/522f74fc91b9ac2ae3a4a8c02d8200e35a91ca4d))
- **process**: add thin Docker Engine API HTTP client ([4c1b04c](https://github.com/rvben/shinyhub/commit/4c1b04c82661a9440100c3d65584f0e70a86932b))
- **process**: add NativeRuntime wrapping exec.Command ([5062064](https://github.com/rvben/shinyhub/commit/50620641a867c3bd0bc5c652f7349a6fa178b033))
- **process**: add Runtime interface, RunHandle, and resource limit fields to StartParams ([a552dc4](https://github.com/rvben/shinyhub/commit/a552dc46879e532e73310370fc9cab47176ae4db))

### Fixed

- **api**: scope env-change restart to running apps and surface failures ([35c11dc](https://github.com/rvben/shinyhub/commit/35c11dc142f56a6facc6714d22a138a8e2e6613b))
- **ui**: allow empty env var values in settings form ([add12fa](https://github.com/rvben/shinyhub/commit/add12fa3ac59810592c573601a311a5016537f96))
- **ui**: move snippet copy button to a toolbar above the code ([0620dff](https://github.com/rvben/shinyhub/commit/0620dff855494315208702f41583e978098cd5a6))
- **ui**: widen modals and make snippet copy button opaque ([32be578](https://github.com/rvben/shinyhub/commit/32be57884462dc94d1ea8f01ae9b3fb8e2931772))
- **ui**: scroll CLI snippets horizontally instead of mid-token wrap ([f3d4e04](https://github.com/rvben/shinyhub/commit/f3d4e04eab06f8fc13ed171bee7b1dd31fb81f50))
- **ui**: default deploy snippet path to "." so it's copy-pastable ([97802f5](https://github.com/rvben/shinyhub/commit/97802f56cf0bae9e2bfe2569aabd5af0ae211179))
- **server**: close readyCh after socket bound, not before listen ([4cc75d1](https://github.com/rvben/shinyhub/commit/4cc75d11ef2d94079136001766a130002560dbd0))
- **db**: scan audit timestamps natively and sweep expired OAuth states ([401bad3](https://github.com/rvben/shinyhub/commit/401bad397889f8a711347f87e74b389408c9ec0d))
- address final review findings — waitContainer timeout, dead code, validation, env vars, tests ([0883383](https://github.com/rvben/shinyhub/commit/088338346c731b2fa52ba297ad14dd0415109ba6))
- **api**: return 200 JSON stopped instead of 503 when handle not found in metrics ([dabe684](https://github.com/rvben/shinyhub/commit/dabe6841bdb70e7b770f97e6c0abe09d7f391803))
- include resource limits in lifecycle watcher deploy path ([0f1e07a](https://github.com/rvben/shinyhub/commit/0f1e07a496bb1fe197a3204aacbf988cdff2b781))
- **api**: serialize nil resource limit fields as JSON null instead of omitting ([76d5761](https://github.com/rvben/shinyhub/commit/76d5761dcc5a7763e325ea88528d9a68dcfc442c))
- **db**: return ErrNotFound from UpdateResourceLimits when slug not found ([85138a3](https://github.com/rvben/shinyhub/commit/85138a36d0a7a7108da22116d5294aa30063d8fe))
- **process**: separate streaming HTTP client, treat 404 kill as no-op ([0da22c7](https://github.com/rvben/shinyhub/commit/0da22c777e77d991348e25b6b68f183ff2fec6e0))
- **process**: handle NewRequest errors, encode filter param, validate waitContainer response ([56bbf40](https://github.com/rvben/shinyhub/commit/56bbf40611ed18695185fcbc08f79635835bd48b))
- **process**: wire Adopt into recovery path, add crash-detection tests, remove stale ESRCH filter ([315bf74](https://github.com/rvben/shinyhub/commit/315bf74bc5da4a37c28504fa0e8251640acd505a))
- **process**: make NativeRuntime.Wait race-safe, add explanatory comments ([62e115f](https://github.com/rvben/shinyhub/commit/62e115f9620e436f8e490e1829f66f797eed5e7a))
- **process**: filter SHINYHUB_* env vars from child process environment ([be28259](https://github.com/rvben/shinyhub/commit/be28259e3bef289f074b4aac7d218d6d0278518b))

## [0.1.0] - 2026-04-16

### Added

- **config**: trust X-Forwarded-For only from configured proxy CIDRs ([8b6a7af](https://github.com/rvben/shinyhub/commit/8b6a7afd0ca4ff5ac0254d21a0bb831f693f6924))
- **audit**: add audit log for all key actions ([005f4e9](https://github.com/rvben/shinyhub/commit/005f4e94021cff26613302472e101a2649c97b26))
- **auth**: add generic OIDC SSO provider ([3926c2a](https://github.com/rvben/shinyhub/commit/3926c2a459103852247b23b7cf2638356db56e33))
- **api**: support targeted rollback to any historical deployment ([f05987f](https://github.com/rvben/shinyhub/commit/f05987f86f4b644b35df87fe5a9c84ae5ef3f77f))
- **deploy**: prune old version dirs after deploy (configurable retention) ([ee45359](https://github.com/rvben/shinyhub/commit/ee4535943feb94ff28d005e0765ada3f042bfda2))
- **lifecycle**: re-adopt live processes on server restart ([f39ff12](https://github.com/rvben/shinyhub/commit/f39ff127491c680dc233351d435d153ead558b27))
- **api**: D1 path-param member revoke, D5 login user info, F6 deploy history, F7 patch name/project_slug, pagination ([862289f](https://github.com/rvben/shinyhub/commit/862289fd94165c815308355380d5ac64f337dd52))
- **api**: add admin user management endpoints F4a-F4d D6 ([a7ba11e](https://github.com/rvben/shinyhub/commit/a7ba11eab67074c4afb79b4bffaa95a352eebb9b))
- **api**: add token list/delete endpoints and name uniqueness check ([96dd1d3](https://github.com/rvben/shinyhub/commit/96dd1d37b6a7fa38d2120dd21c852794eef6d9b4))
- **api**: add app lifecycle endpoints F1 F2 D2 D3 ([045a4e7](https://github.com/rvben/shinyhub/commit/045a4e743dbef01c83f7e502318ecfa5206ee4c6))
- **ui**: redesign dashboard with Ember Dark aesthetic — Syne/JetBrains Mono fonts, amber accent, cohesive dark modal, animated cards ([f82afbf](https://github.com/rvben/shinyhub/commit/f82afbf1c2c9010409af75915a4bd517fcda15e1))
- **ui**: add access control modal for managing app visibility and members ([87d4137](https://github.com/rvben/shinyhub/commit/87d4137d7a53a871b6b3b35689d4da25ad00cc73))
- **api**: add GET /api/apps/{slug}/members and GET /api/users endpoints ([10b8d40](https://github.com/rvben/shinyhub/commit/10b8d40904fa38d9c5dc5f4a086a197d9f84455b))
- **db**: add role column to app_members; update GetAppMembers to return username and role ([3bc3e35](https://github.com/rvben/shinyhub/commit/3bc3e3579995bff029b1016a748dc9bc99048e9c))
- **ui**: add Sign in with Google button to login page ([f2264e1](https://github.com/rvben/shinyhub/commit/f2264e12f04bf303e5af9f60f9d66aa98c2e18e5))
- **api**: add Google OAuth2 login and callback handlers ([2be257f](https://github.com/rvben/shinyhub/commit/2be257f04ada9bc8d7460a406c0cba72b31044ce))
- **config**: add Google OAuth2 configuration support ([f2b3c0a](https://github.com/rvben/shinyhub/commit/f2b3c0abb213164098f112c5bdda195fdca9345e))
- **oauth**: add Google OAuth2 provider ([dd1daf5](https://github.com/rvben/shinyhub/commit/dd1daf5f615dd1281978cb32c5309dab58e4146c))
- add install.sh for one-liner binary installation ([a86bc4a](https://github.com/rvben/shinyhub/commit/a86bc4a2a73a6fc9e5b32d495e7e5ccddace043f))
- add GoReleaser config for multi-platform binary releases ([a9686db](https://github.com/rvben/shinyhub/commit/a9686db78ddc04de1ccc28b942c3a37d7c4585a3))
- add version variable injected at build time via ldflags ([223fe4f](https://github.com/rvben/shinyhub/commit/223fe4f41026e11d8e53de69c0b5f9732bfd470a))
- **ui**: poll and display CPU/memory metrics per running app ([a06f8d0](https://github.com/rvben/shinyhub/commit/a06f8d0ee6e066c9400522a13bcdec562bd537e2))
- **api**: implement GET /api/apps/{slug}/metrics for CPU and memory stats ([3a666ad](https://github.com/rvben/shinyhub/commit/3a666ad9ff80632a6cf91a8423eec36f16060321))
- **api**: wire Sampler into Server; add Manager.Get; register metrics route ([291878b](https://github.com/rvben/shinyhub/commit/291878b5d577f8b985870958ad3ef5f245224a48))
- **process**: add Sampler interface and gopsutil-backed Stats for resource metrics ([700a0e1](https://github.com/rvben/shinyhub/commit/700a0e171a6aba3b7272ff76d4ac77f8a8ea2349))
- **ui**: add Logs button and full-screen SSE log pane ([f5dbfad](https://github.com/rvben/shinyhub/commit/f5dbfadfc18803ddd4d021fe91de5722a3f35d3e))
- **api**: implement real log streaming via SSE with 200-line initial burst ([79c20e1](https://github.com/rvben/shinyhub/commit/79c20e185d72b00cc406240697edab7cc84a517d))
- **process**: wire LogFile into Manager; capture app stdout/stderr to disk ([2fb0c47](https://github.com/rvben/shinyhub/commit/2fb0c4741e93a04304550efa5843d5bcc876c1d4))
- **process**: add LogFile and LogReader for per-app log capture ([9a79ef7](https://github.com/rvben/shinyhub/commit/9a79ef75e466a79d8631b1cbf47fc8cac35e2a54))
- **ui**: rewrite frontend in vanilla JS with session cookie auth ([3b2f99a](https://github.com/rvben/shinyhub/commit/3b2f99ac83adeb239328dc657d3d5254dc3950a6))
- **lifecycle**: propagate deploy result so watcher persists port and PID ([69f04a8](https://github.com/rvben/shinyhub/commit/69f04a8b2153161d61220fa32e31e9d1cf6293ae))
- **api**: enforce bundle upload size limit with http.MaxBytesReader ([16ec224](https://github.com/rvben/shinyhub/commit/16ec224c701e1084d00e51d4100709a9a4daace9))
- **api**: register session auth routes and switch OAuth callback to cookie ([279769e](https://github.com/rvben/shinyhub/commit/279769e28706cc400bf330b04bb2e7454df4411c))
- **db**: add access control, member grant, and visibility queries ([d27b632](https://github.com/rvben/shinyhub/commit/d27b63234c82727a01dcba6a7180e4b5d4143b35))
- **auth**: add HttpOnly session cookie management and dual-mode middleware ([ce29b8d](https://github.com/rvben/shinyhub/commit/ce29b8df8083df5aa9f547d87ea423cd102105a8))
- **api**: add session cookie auth, per-app access control, and auth fixes ([e3fa2b8](https://github.com/rvben/shinyhub/commit/e3fa2b8dbf7c9abaa6cd6f04a0ba8a44b4f06f35))
- GitHub OAuth2 login with CSRF state, user provisioning, and SPA token redirect ([5920235](https://github.com/rvben/shinyhub/commit/5920235726975e989ff5d6faa937d1e14fda9b96))
- **ui**: add Login with GitHub button and token redirect handling ([ce4229a](https://github.com/rvben/shinyhub/commit/ce4229a23a9175974ab2d224ae18a911e8f268ef))
- **api**: add GitHub OAuth2 login and callback handlers ([c921d0a](https://github.com/rvben/shinyhub/commit/c921d0ab92c17b613ca0ea0a625316c6c41d1390))
- **oauth**: add GitHub OAuth2 provider with AuthURL, Exchange, FetchUser ([e85ca23](https://github.com/rvben/shinyhub/commit/e85ca23180e3680950f6bf0b911c56d68634f697))
- **config**: add OAuthConfig for GitHub OAuth2 credentials ([d55429e](https://github.com/rvben/shinyhub/commit/d55429e925fbd12e0788338f767a1f3bb5459ec4))
- **db**: add oauth_accounts and oauth_states tables with queries ([d1f2d53](https://github.com/rvben/shinyhub/commit/d1f2d53020fd55f8e272d699aadead79cdf872ae))
- per-app access control with JWT middleware and app_members grants ([4c95502](https://github.com/rvben/shinyhub/commit/4c955027e4c8f4fa22c3d1547f4653f9fb05d614))
- **ui**: set shiny_session cookie on login for private app access ([0bc9627](https://github.com/rvben/shinyhub/commit/0bc96276f03e4e8b932a2ee04fbe367c78090863))
- **access**: wire access control middleware; add API routes and CLI access subcommand ([4b6cb38](https://github.com/rvben/shinyhub/commit/4b6cb38482709e0ab57d762d845523700b93e614))
- **access**: add per-app access control middleware with JWT + cookie auth ([53eb68f](https://github.com/rvben/shinyhub/commit/53eb68ff6a373e104d778427a593e61a0a51c7fd))
- **db**: add app_members table for per-user access grants ([1a7017d](https://github.com/rvben/shinyhub/commit/1a7017ddaeb8b29e896ea037aec3be5822fa3eef))
- **cli**: add --git, --branch, and --subdir flags to deploy command ([5e47d3a](https://github.com/rvben/shinyhub/commit/5e47d3a01d0527ff70079db40161ffc86c483e5b))
- **deploy**: add R Shiny support via app.R detection and Rscript command ([94b2294](https://github.com/rvben/shinyhub/commit/94b2294d3a6f57147af013ce927870cf757081c0))
- **process**: add SyncR for renv::restore() dependency sync ([d5c80d8](https://github.com/rvben/shinyhub/commit/d5c80d85d1a81210dd8e9a45eae418629ac1e997))
- **main**: wire up lifecycle.Watcher for watchdog and hibernation ([cba85da](https://github.com/rvben/shinyhub/commit/cba85dafd5979b054e4f3d2a5341a9f3aaaf924a))
- **cli**: add 'apps set --hibernate-timeout' subcommand ([95b11de](https://github.com/rvben/shinyhub/commit/95b11de5e99741a81bded0081a75ae84a701b809))
- **api**: add PATCH /api/apps/{slug} for hibernate_timeout_minutes ([0690a1c](https://github.com/rvben/shinyhub/commit/0690a1c6a9f57604f9fef6faf8b317013d8f068c))
- **lifecycle**: add Watcher with watchdog, hibernation, and wake-on-request ([b476b0e](https://github.com/rvben/shinyhub/commit/b476b0e82ece7cb6e6df3a785c25fea45153708b))
- **proxy**: add activity tracking, loading page, and onMiss callback ([9e3dfce](https://github.com/rvben/shinyhub/commit/9e3dfceb5f8a7adc4715f769c5a6ba6524a521d0))
- **config**: add LifecycleConfig with duration parsing and defaults ([2cfda97](https://github.com/rvben/shinyhub/commit/2cfda974d066a6306f730f8d9bf969a8462925a1))
- **db**: add hibernate_timeout_minutes column and UpdateHibernateTimeout query ([2a38fec](https://github.com/rvben/shinyhub/commit/2a38fec158ec296128b90aea2ec13f54118b181f))
- embed htmx + Alpine.js web UI with app list, login, restart, rollback ([b69287f](https://github.com/rvben/shinyhub/commit/b69287f5c10a58d7391e6cf388e6fd6514e4edfa))
- shiny CLI — login, deploy, apps list/logs/rollback/restart, tokens ([54adb1b](https://github.com/rvben/shinyhub/commit/54adb1b6b8f727c0baa7ab6696ccff142aceb941))
- wire main binary — db, process manager, proxy, API, admin bootstrap ([2d94a7f](https://github.com/rvben/shinyhub/commit/2d94a7f71dbec212bca30fcb56108c05ca309b9c))
- API router — auth, apps CRUD, deploy, rollback, restart, SSE logs ([56b1af3](https://github.com/rvben/shinyhub/commit/56b1af339d0def279730e93fee4b2276002adbe4))
- **deploy**: add deploy orchestrator with bundle extraction and proxy swap ([6e2d6ab](https://github.com/rvben/shinyhub/commit/6e2d6ab9ba847a33d1a051eb1633c0b46329a1b3))
- reverse proxy with atomic slug→backend routing table ([7ea5266](https://github.com/rvben/shinyhub/commit/7ea52661a07ee8137972b905c3deef439a3a839e))
- process manager — spawn, track, stop app processes; uv wrappers ([5044661](https://github.com/rvben/shinyhub/commit/504466185c500ca02fee38c2ed7c2a142f758ed8))
- auth — bcrypt, JWT, API key hashing, bearer middleware ([3942928](https://github.com/rvben/shinyhub/commit/39429285af80503997a256739af2fbc1ebdce5c8))
- SQLite store with users, apps, deployments schema ([0614272](https://github.com/rvben/shinyhub/commit/061427243df713e82301a5e46af00f194aa33bdb))
- project scaffold, config loader, build targets ([7a9ba5f](https://github.com/rvben/shinyhub/commit/7a9ba5f9430442a8083838516b3d9fa95e26ada5))

### Fixed

- **deploy**: log pruning errors instead of silently dropping them ([b69c7cf](https://github.com/rvben/shinyhub/commit/b69c7cf2a933feac064408f8063110f47bc46c36))
- **audit**: add OAuth/OIDC login events, fix rate-limiter IP key, complete coverage ([574fdbd](https://github.com/rvben/shinyhub/commit/574fdbdd03710a8df388f098186db78332d5ca94))
- **auth**: improve OIDC username collision handling and response headers ([83a6f7c](https://github.com/rvben/shinyhub/commit/83a6f7c36339252e7f2dcc27ceb39d49e8d2ba57))
- **deploy**: protect active bundle zip, return prune errors, fix skip accounting ([a5af488](https://github.com/rvben/shinyhub/commit/a5af488ca9d5a2333877fd7010e0cd0918cd7125))
- **lifecycle**: handle proxy registration failure in process recovery ([427b37a](https://github.com/rvben/shinyhub/commit/427b37a2e0d6676b0df088cfe1282e48aacb6a0f))
- **api**: validate all PATCH fields before writing, paginate ListApps, sort deployments by id ([8049108](https://github.com/rvben/shinyhub/commit/804910894cbae38b1934da26c7a8eba47d09427b))
- **auth**: reduce JWT TTL, add login rate limiting, fix slug enumeration ([4776af9](https://github.com/rvben/shinyhub/commit/4776af9de23f0e5c5fa8b1f1e2e81bf9b3e216e0))
- **ui**: accessibility and responsiveness improvements ([ba89dda](https://github.com/rvben/shinyhub/commit/ba89dda984c576beb2eccd6b0be55c9e8fb99adf))
- **auth**: enforce shared app visibility and manager member role ([7268661](https://github.com/rvben/shinyhub/commit/7268661ae1a2fa950d143417f7f2cb5bdeb8b8ff))
- **api**: address B1 B2 B5 B6 B7 H3 D4 B10 ([1157993](https://github.com/rvben/shinyhub/commit/115799389a73cacfbc96ec8c7eff42a1b0e6a85d))
- **api**: replace http.Error with JSON error responses ([e1c74cf](https://github.com/rvben/shinyhub/commit/e1c74cf23b834f17ffaf935d377473e0f7bff3f8))
- **ui**: improve loading states, accessibility, and UX ([04c0cfa](https://github.com/rvben/shinyhub/commit/04c0cfaabc5f5204e29ec6cb44c9d31299eecd91))
- **api,ui**: remove role from user lookup response; guard radio PATCH against failures ([818895e](https://github.com/rvben/shinyhub/commit/818895e4a68c33af65bb061e55b76013204f8253))
- **ui**: prevent modal-overlay display:flex from overriding hidden attribute; fix revoke handler closure race and error handling ([98cdc85](https://github.com/rvben/shinyhub/commit/98cdc85ba2d55d379d63000c267700079a768c9d))
- **access**: operator role now bypasses per-app access check in proxy middleware ([c6d640d](https://github.com/rvben/shinyhub/commit/c6d640d721112fc6a48c7564af8233b2022027e5))
- **db**: return empty slice from GetAppMembers when app has no members ([361acb3](https://github.com/rvben/shinyhub/commit/361acb3dc9ca35e9a94a3f37d5b70fa4d6f6e61e))
- use POSIX-safe sudo in install.sh; evict dead PIDs from GopsutilSampler cache ([c776a44](https://github.com/rvben/shinyhub/commit/c776a440390f6cac65ff1498cfe39b6f4b86d11c))
- **api**: use process.StatusStopped constant in handleMetrics sampler-error path ([186a3db](https://github.com/rvben/shinyhub/commit/186a3dbece52268a49b9a38eece3afa46cf31aa8))
- **api**: add StatusUnknown constant and test all handleMetrics paths ([c8fb651](https://github.com/rvben/shinyhub/commit/c8fb651654854ca3a6f9bbed34a566ca85e645f9))
- **process**: cache gopsutil process handles for real CPU deltas; guard RSS overflow ([f139a80](https://github.com/rvben/shinyhub/commit/f139a8065e072c8f058b8391303cf19efad15111))
- **ui**: close EventSource on logout; use createTextNode for log lines ([62b4e91](https://github.com/rvben/shinyhub/commit/62b4e91928acf7803626bfb3596bbe1f45b77b1d))
- **api**: assert SSE data prefix in log test; log Tail errors ([548182a](https://github.com/rvben/shinyhub/commit/548182afe3b067fd2244148660c500b7e5e8f782))
- **process**: set StatusStopped instead of StatusCrashed on clean Stop ([03b8905](https://github.com/rvben/shinyhub/commit/03b8905450e18f8f9d9ce46ec66786906a639de3))
- **process**: harden LogFile rotate error paths and guard Tail(0) panic ([345ab79](https://github.com/rvben/shinyhub/commit/345ab7925a94b272be4eaf1b3cbf03a5aacbc218))
- **access**: use AuthenticateRequest so session cookie is accepted at proxy ([8f20c7f](https://github.com/rvben/shinyhub/commit/8f20c7fc0f38e227d70346c41878db20c3d4d46e))
- **api**: check GitHub provider nil before consuming CSRF state in callback; add not-configured test ([af0ffd6](https://github.com/rvben/shinyhub/commit/af0ffd6385447ccf5e3cb7b50b4cd040b0813f1c))
- **access**: return 404 for unknown slug in SetAppAccess; add missing 403 test and shared/private comment ([eb6fa74](https://github.com/rvben/shinyhub/commit/eb6fa743740ec15f7c86334dc3e67f2ba88450d2))
- **cli**: prevent temp dir leak when subdir not found in cloned repo ([2747a3a](https://github.com/rvben/shinyhub/commit/2747a3aa457458eecb7268c797bd4e0de8742f67))
- **deploy**: remove unused file creation and add error checks in tests ([31d61ad](https://github.com/rvben/shinyhub/commit/31d61ad7f6fd1093c13d125e67d413dae3c2d314))
- **process**: use errors.Is and exec.LookPath in SyncR ([f871fd1](https://github.com/rvben/shinyhub/commit/f871fd163b32da46850c5e89e7a7b1141cc11459))
- **main**: explicitly cancel context before log.Fatal to signal Watcher shutdown ([a42f0b7](https://github.com/rvben/shinyhub/commit/a42f0b7448f351b77c3cb351b3c82eaa78d202e8))
- **cli**: use Flags().Changed() instead of sentinel and clarify flag description ([8aeeadf](https://github.com/rvben/shinyhub/commit/8aeeadf544ff13abadb184a37cb9c9bd49a50204))
- **api**: return 404 when patching non-existent app slug ([15481e5](https://github.com/rvben/shinyhub/commit/15481e5c465c1c4227b4fcb2b427e9d59ceb7698))
- **lifecycle**: improve test robustness — map-keyed fakeStore and config-driven loop ([3dba059](https://github.com/rvben/shinyhub/commit/3dba0592554f56af82a109d29cf2b120c390a6d5))
- **lifecycle**: use delete for attempt reset and add interface guards ([82f35d7](https://github.com/rvben/shinyhub/commit/82f35d7663ecf4889d205369efddc2fd471ab4cf))
- **proxy**: use RWMutex for lastSeen reads and channel sync in test ([2cafdf6](https://github.com/rvben/shinyhub/commit/2cafdf6726fe2de1eaed2190791887627179bd81))
- four issues found by Codex review ([29f3f38](https://github.com/rvben/shinyhub/commit/29f3f38c1010eef6cac56bfca7703fdd80fc431e))
- correct Alpine.js script load order and add missing badge style ([b22b56c](https://github.com/rvben/shinyhub/commit/b22b56cc27f7dcc917ae99864d27395b54417f01))
- correct uv invocation and remove invalid shiny --workers flag ([4acc529](https://github.com/rvben/shinyhub/commit/4acc529e3a7cd6f9c869e3e2188cd7e0869fa9a3))
- JWT sub claim conflict, API key auth scheme, empty admin password guard, rollback deployment record, deployment ordering ([c992764](https://github.com/rvben/shinyhub/commit/c99276426712b12e59f1221d7fe08b1c9988a01a))
- tokens create CLI command, config file permissions, status update error logging, proxy comment ([bcba168](https://github.com/rvben/shinyhub/commit/bcba1684445f0bb6ddf842aabf2c4aaec2a209e1))
- integration test resource cleanup and error handling ([352b92f](https://github.com/rvben/shinyhub/commit/352b92f3773bcb4f0b0d555c7cd6572692207166))
- make Alpine auth store reactive, add network error handling to mutation calls ([64ff458](https://github.com/rvben/shinyhub/commit/64ff458b4677bbf012e865fb1bfdc91c0573c90a))
- improve UI error handling, fix Alpine CDN URL, use Alpine store for auth ([3319a65](https://github.com/rvben/shinyhub/commit/3319a65c2ddd122a1f94601cb1c2c4ebb18c7aaf))
- align deploy multipart protocol, skip .git in zip, add json tags, http timeouts ([1001ee1](https://github.com/rvben/shinyhub/commit/1001ee10aae5cfd0f5909e1059c55e3396eda701))
- main — defer Close before Migrate, ErrNotFound check for admin bootstrap ([ce03e80](https://github.com/rvben/shinyhub/commit/ce03e803bd444501ee801b14d70a1833fa8ee8bc))
- API — remove bundle_dir from request, generic error messages, constant-time login, writeJSON helper ([c8e6fac](https://github.com/rvben/shinyhub/commit/c8e6facce0b6557a597234a55f595fa451766bf1))
- deploy — context-aware health check, injected HealthCheck fn, errors.Join cleanup, zip-slip error ([074e355](https://github.com/rvben/shinyhub/commit/074e355442098196a6ba107d788a5881daef9909))
- proxy — validate url in Register, strip RawPath, remove dead targets map ([6b967ef](https://github.com/rvben/shinyhub/commit/6b967efe39660528eea375bff1b15579204a138c))
- process manager — guard empty command, wait for exit on stop, add tests ([d226d88](https://github.com/rvben/shinyhub/commit/d226d88eb3845903d66dd336bcc8aeb270e3c58c))
- **auth**: RequireRole unknown-role vulnerability, correct status codes, add missing tests ([cec2b57](https://github.com/rvben/shinyhub/commit/cec2b57994b646a47c70953607c675ffafa712de))
- **db**: enable FK enforcement, fix test setup, consistent error wrapping ([37d0fa3](https://github.com/rvben/shinyhub/commit/37d0fa370f289d65ff3e2cd0f7a76792b6e6b1c0))
