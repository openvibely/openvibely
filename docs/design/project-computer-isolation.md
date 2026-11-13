# Project computer task isolation

The project keeps one Docker container and one durable home volume. Each task
gets a separate Linux PID, mount, IPC and network namespace; it is not a separate
Docker container. The Computer tab and the application URLs are unchanged.

## Access boundaries

* Only the root supervisor listens on the container's published TCP port. Every
  request, including loopback requests, requires the application bearer token.
  This router has no shell, file, screen or browser endpoints of its own.
* Each task bridge listens on `/run/ov-desktops/<task>/control.sock`. The enclosing
  directory is root-only and the socket is mode 0600. The router proxies HTTP
  and viewer WebSockets over this socket. The token is not passed to task
  processes or networking helpers.
* Each desktop and its shell jobs have separate, automatically assigned Linux
  UIDs. They have no login passwords. A root-owned persistent identity registry
  rejects UID collisions; restarting a task preserves its identity and data.
* Desktop applications, VNC, websockify and screenshot/browser helpers drop all
  capabilities and set `no_new_privs`. Model shell jobs use the separate shell
  UID, a minimal environment, and no display authentication or control secrets.
* Browser profiles and desktop config are accessible only to that task's desktop
  UID. The file bridge remains confined by `os.OpenRoot`; files created through
  the bridge are assigned to the shell user and made accessible to the desktop.
* X11 filesystem and abstract sockets, CDP and VNC ports exist only inside their
  task's namespaces. X11 requires an authority cookie inaccessible to shell jobs.
  The task firewall blocks shell-owned connections to all local IPv4/IPv6
  destinations. slirp4netns supplies outbound Internet access without sharing the
  supervisor's loopback network. The shell cannot alter those firewall rules.
* A task's `/proc` exposes only that task's processes, with `hidepid=2`. Its `/tmp`
  and shared memory are private. Its users cannot inspect or signal the trusted
  root bridge or processes belonging to another task.

Application ownership, sharing permissions and takeover checks remain in OV.
The socket/namespace boundary prevents task commands from bypassing those checks
by directly calling another task's desktop or browser. Private takeover cancels
shell jobs and kills all shell-UID processes, including children that detached
from their original process group. The user's authorized viewer stays connected.

This isolates computer tasks from each other; it does not sandbox tools the user
explicitly chooses to run on the host, an operator with Docker/root access, or
arbitrary malicious code already installed inside a task's own graphical apps.
The root supervisor and Linux kernel remain trusted components.

## Runtime requirements and upgrade

The container's trusted supervisor needs `SYS_ADMIN`, `NET_ADMIN`, and
`/dev/net/tun` to create namespaces and configure outbound networking. The local
provisioner supplies these explicitly, keeps `no-new-privileges`, and disables
the default AppArmor profile because it denies the namespace mounts. It does
**not** use Docker privileged mode or expose the Docker socket. Untrusted task
processes retain none of these capabilities. Hosted/custom-image operators must
supply the same runtime configuration and the updated image; unsupported hosts
fail startup rather than falling back to shared-user desktops.

New images advertise `isolation: 1` in their health response. OV rejects legacy
local/hosted images without it. The local container isolation label also forces
replacement of an old container even when a custom image tag is reused.

The home volume is retained during replacement. On a task's first start, its
existing task directories are locked before the router accepts requests, including
tasks that have not started yet. Legacy hard links are broken into independent
files. The `home`, `browser`, `config`, and `cache` directories are assigned to the
new identities without following symlinks. Regular files and browser profiles are retained in place; only hard-linked files
are copied to remove shared inodes. Legacy project-wide browser data is retained but not exposed
to task users. Unsafe storage symlinks or identity collisions fail closed.
Downgrading to an older shared-user image is unsupported because ownership has
changed; keep a volume backup when operating custom images.

Task shutdown gracefully closes Chromium before terminating the task namespace.
Stopping/restarting the project computer preserves task files and profiles.

## Verification

Build the actual runtime and run disposable-container boundary tests:

```sh
docker build -t ov-computer-isolation -f packaging/computer/Dockerfile .
python3 scripts/test-computer-isolation.py ov-computer-isolation
OPENVIBELY_TEST_COMPUTER_IMAGE=ov-computer-isolation go test ./internal/service \
  -run 'TestProjectComputer(LocalDockerLifecycle|TaskDesktopsLive)$' -count=1 -timeout=240s
```

The boundary script uses a new volume and removes only its own test resources.
It checks unauthenticated loopback rejection, cross-task files, protected sockets,
CDP/VNC/X11 denial from a shell, credential stripping, privilege restrictions,
authorized screenshots/input/CDP/viewer connections, outbound Internet access,
separate login cookies, detached-process cleanup,
legacy file ownership migration, and persistence across task/container restart.
Linux bridge unit tests also cover the fail-closed identity/environment paths.
