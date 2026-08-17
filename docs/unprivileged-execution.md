# Running launcher unprivileged

Notes from investigating why launcher does not run correctly as a non-root user on Linux,
plus the harness used to reproduce it.

## The reported symptom

> osqueryd runs, starts, and deletes its own management socket then sits there with nothing
> to do.

This is osquery's normal behavior when the extension it was told to require never shows up.
Launcher always starts osquery with:

```
--config_plugin=kolide_grpc
--extensions_require=kolide_grpc
--extensions_timeout=20
```

`kolide_grpc` is launcher's own extension -- it provides osquery's config, distributed
queries, and log destination (see `KolideSaasExtensionName` in
`pkg/osquery/runtime/osqueryinstance.go`). So if launcher cannot register that extension,
osquery has no config plugin and nothing to do.

The sequence, from osquery's source:

1. `Initializer::start` calls `startExtensionManager`, which binds the manager socket
   (`osquery/extensions/extensions.cpp`).
2. Because `--extensions_require` is set, osquery polls for `kolide_grpc` for
   `--extensions_timeout` seconds, then gives up:
   `Required extension not found or not loaded: kolide_grpc`.
3. The `ExtensionManagerRunner` is torn down. `~ExtensionRunnerInterface` calls
   `removePath(path_)` (`osquery/extensions/impl_thrift.cpp`) -- **this is osquery deleting
   its own management socket**.
4. `initActivePlugin("config", "kolide_grpc")` fails and osquery requests shutdown with
   `EXIT_CATASTROPHIC`. The worker exits, then the watcher exits.

Reproduced against osquery 5.23.1 by starting osqueryd with launcher's flags and no
extension provider. The socket disappears several seconds before the process does, so the
observable state is exactly "socket gone, osqueryd still running, doing nothing":

```
t=10s socket=present osqueryd_procs=4
t=15s socket=GONE    osqueryd_procs=2
t=25s socket=GONE    osqueryd_procs=0
```

The takeaway: **the deleted socket is a downstream symptom.** The thing to debug is why
launcher failed to register `kolide_grpc`.

## Why registration fails unprivileged: extension socket path length

osquery binds the manager socket at `--extensions_socket`. Every registered extension --
including launcher's -- binds at `<extensions_socket>.<uuid>`, where `uuid` is an osquery
route UUID of up to five digits (`getExtensionSocket` in
`osquery/extensions/extensions.h`). Unix domain socket paths are capped by `sun_path`:
108 bytes on Linux, 104 on darwin.

Launcher used to name the socket `<root>/osquery-<26-char ULID>.sock`, which is 40
characters of overhead on top of the root directory. That leaves room for the default
root-owned install path, but not much else:

| root directory | length | manager socket | extension socket |
| --- | --- | --- | --- |
| `/var/kolide-k2/k2device.kolide.com` | 34 | 74 -- fits | 80 -- fits |
| `/home/kolide/.local/share/kolide-k2/k2device-preprod.kolide.com` | 62 | 102 -- fits | 108 -- **too long** |

That middle band is the nasty case: osquery binds the manager socket fine, launcher's
extension server cannot bind, and the failure surfaces as osquery deleting its own socket.
Unprivileged installs land in that band precisely because they cannot use `/var` and end up
under a home or XDG directory instead.

`pkg/osquery/interactive` already worked around this by truncating its socket ID; the
runtime path now does the same, and `calculateOsqueryPaths` validates the suffixed length so
a genuinely too-deep root directory fails immediately with an actionable error instead of
looping.

## Why startup fails unprivileged: chmod of the root directory

`runLauncher` chmods the root directory (and, for `/var/kolide-k2` children, its parent) to
0755 so that user-context desktop processes can read it. `chmod` requires ownership, so when
launcher runs as a non-root user against a root-created install directory this returned an
error and launcher refused to start:

```
run launcher: chmodding root directory: chmod /var/kolide-k2/k2device.kolide.com: operation not permitted
```

The mode is a nice-to-have, not a requirement, so this is now logged and startup continues.
Writability *is* a requirement, so it is now checked explicitly and fails early with a clear
message naming the directory and the effective uid.

## What is *not* the problem

Worth recording, because these were the first suspects:

- **osqueryd itself runs fine as a non-root user.** It creates and keeps its extension
  socket; missing privileged inputs (`/etc/osquery/extensions.load`, `/var/log/osquery`)
  produce warnings, not failures.
- **osquery's `safePermissions` gate is satisfied.** The watchdog re-execs the worker only
  if `safePermissions(dir(osqueryd), osqueryd, true)` passes
  (`osquery/core/watcher.cpp`). On POSIX that requires the binary to be owned by root *or
  the current user*, executable by its owner, and not world-writable. A packaged install
  (root-owned) and a TUF-library install (launcher-owned, chmod 0755 by
  `ee/tuf/library_manager.go`) both pass. It only fails if osqueryd is owned by some third
  user, or sits in a sticky-bit directory such as `/tmp` -- `platformIsTmpDir` in
  `osquery/filesystem/filesystem.cpp`. That is what `--allow_unsafe` exists for; launcher
  passes it on Windows only, and does not need it for the normal unprivileged cases.
- **The osquery runtime test suite passes as a non-root user**, including the
  watchdog-enabled tests.

## Still root-only

These are unchanged and remain reasons an unprivileged launcher is degraded rather than
broken:

- The desktop runner spawns per-console-user processes, which needs root to switch users.
- Hardware-backed keys (TPM on Linux, Secure Enclave on darwin) need device access; launcher
  already falls back to a local key.
- `permissions.RestrictFileAccessToRootOnly` on `launcher.db` is best-effort and already
  logs rather than failing.
- Any osquery table that needs privileged reads returns less data.

## The harness

Two pieces, both added for this investigation:

- `tools/mock-kolide-server` -- stands in for Kolide SaaS. It implements the JSONRPC methods
  in `pkg/service/client_jsonrpc.go` (`RequestEnrollment`, `RequestConfig`, `RequestQueries`,
  `PublishLogs`, `PublishResults`, `CheckHealth`), hands osquery a config with a schedule,
  and returns a set of distributed queries covering both osquery core tables and launcher's
  own extension tables (`kolide_launcher_info`, `launcher_gc_info`, `osquery_extensions`).
  Results are printed as `RESULT <query> status=... rows=...`, so a working end-to-end run is
  obvious and a half-working one (osquery up, launcher tables missing) is distinguishable.
- `tools/run-unprivileged.sh` -- builds both binaries, creates an unprivileged user, runs
  launcher as that user against the mock server, and samples the manager socket and osqueryd
  process count each second, flagging the "socket gone while osqueryd is still running"
  state.

```
go run ./tools/download-osquery.go -platform linux -arch amd64 -output /usr/local/bin/osqueryd
sudo ./tools/run-unprivileged.sh --seconds 60
```

To point it at a specific layout -- for instance to reproduce the socket-length band -- pass
`--root-dir`:

```
sudo ./tools/run-unprivileged.sh --root-dir /home/kolide/.local/share/kolide-k2/k2device-preprod.kolide.com
```

A healthy run publishes results for every query on every distributed interval and holds
`manager_sockets=1` for the whole run.
