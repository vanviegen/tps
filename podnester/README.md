# podnester

Give a container a Docker socket of its own, backed by the host's podman,
and let it use nothing through it that it could not reach already.

A rootless podman container cannot run containers of its own without
loosening the sandbox around it (nested user namespaces need capabilities
and unmasked `/proc`, and the inner storage cannot share the host's images).
Handing it the host's podman socket is the other extreme: that is the host.
podnester sits in between. It serves the container a socket speaking the
Docker API — so `docker`, `docker compose`, testcontainers and anything else
that talks to `DOCKER_HOST` just work — and passes each request on to podman
only after holding it to what that container may have:

- Containers, volumes and networks it makes are labeled as its own and get its
  name as a prefix on the host. It sees and touches only those; the prefix is
  stripped from every answer, so it sees the names it chose.
- A bind mount source is a path *in that container*. The container itself
  resolves it (`readlink -f`, so a link to `/home/user/.ssh` means its own
  `/home`, not the host's), and the result is mapped to the host path naming
  the same file: under one of the container's own bind mounts, or in its root
  filesystem, which podman keeps mounted on the host while it runs.
- Published ports are published *in that container*, the way a mount source is
  found there: the proxy runs a small forwarder inside it for each, so
  `-p 5432:5432` makes `localhost:5432` work exactly as it would on a host.
  `-p 0:5432` picks a free port and `docker port` tells which. The forwarders
  speak tcp: a binding of any other protocol is ignored, with a warning on the
  create that asked for it. A sibling on a network of its own (compose makes
  one per project) is reached there: the top-level container is on every
  network made through the proxy.
- Sub-containers join a bridge network of the top-level container's, which
  it is on as well, so they reach each other and it reaches them by name;
  `docker network create` makes further networks it joins too (what compose
  does). Sub-containers live in the top-level container's own user namespace
  (and get its SELinux settings), so uids mean the same everywhere: a file
  written as root in one is root's in the top-level container too, and its
  own uid is the same uid in them. That is also what lets them join each
  other's network, pid and ipc namespaces (`--network container:app`), as
  the kernel allows that only within one user namespace. A sub-container
  runs as its image's user, or root, as it would under docker.
- Everything that would reach past the container is refused: privileges,
  capabilities, devices, sysctls other than the namespaced ones (`net.*`
  and the IPC ones, docker's rule), the host's namespaces, registry logins,
  image removal (images are the host's, shared by all), host paths in volume
  and log drivers, mount propagation into the host, and every endpoint and
  every field of a container spec the proxy does not know. Podman's own
  (libpod) API is not served: the docker CLI is the client. A `podman` that
  is a symlink to it works for the usual commands, as the CLIs mirror each
  other; a real podman with `CONTAINER_HOST` set gets a clear pointer to docker.
- Sub-containers are removed when the top-level container stops, like the
  processes in it, and so are the networks made for them: a container coming
  back is on none of them, and has them made again, and joins them, when
  asked. Its volumes stay for its next run; `purge` removes those too.

## Running

podnester is podman, except that its `run` gives the container docker:

```sh
podnester run --rm -it --name mytask --userns keep-id:uid=1000,gid=1000 \
    --security-opt label=disable -v "$PWD:/work" -w /work myimage bash
```

That is a `podman run` with `--network mytask-bridge`, the mount of the
directory holding the socket and `DOCKER_HOST`/`CONTAINER_HOST` pointing at
it added. `--name` and `--security-opt` are read off the options (the
container's sub-containers share its user namespace and get the same
security options, so shared files show the same owners); without a name one
is made up. With `-d`, podnester keeps serving the socket in the background for as
long as the container exists; otherwise it serves while the container runs
and removes what it started when it exits. Every other command (`podnester
ps`, ...) is podman's, so `alias podman=podnester` is an option. Put a
static `docker` in the image or on a mount; `DOCKER_BUILDKIT=0` is set, as
podman builds without buildkit.

`podnester serve X [--security-opt ...]` serves the socket for
a container run by something else, which must be started with `--network
X-bridge`, the control directory (`$XDG_RUNTIME_DIR/podnester/X`) mounted at
`/run/podnester`, and `DOCKER_HOST=unix:///run/podnester/podman.sock`.
`podnester purge [--volumes] X` removes what X made.

Podman's API is expected on `$XDG_RUNTIME_DIR/podman/podman.sock`; when
nothing answers there, a `podman system service` is started on it.

## As a library

```go
p, err := podnester.New(podnester.Config{
    Upstream: sock, Owner: name, Control: dir, ControlMount: "/run/podnester",
    SecurityOpt: []string{"label=disable"},
})
p.EnsureNetwork(ctx)              // then run the owner with --network p.Network()
go p.ListenAndServe(ctx)          // p.Socket() on the host, p.SocketMount() inside
p.Purge(ctx, true)                // when the owner is gone for good
```

The binary embedding the proxy must call `podnester.Subcommand(os.Args)`
first thing in `main` and be built static (`CGO_ENABLED=0`): a copy of it is
placed in the control directory to run as the port forwarder inside the
container.

## Caveats

- Resolving a bind source and podman mounting it are two steps; a container
  replacing a directory with a symlink in between could point podman at a
  host path. The host is the same user's, so what is at stake is that user's
  files. A mount source must exist in the container (readlink -f resolves
  what is there), and the container needs `readlink` (coreutils or busybox).
- Images are shared by every container on the host, and pulling or building
  one is visible to all: an image name is not prefixed. Registry logins are
  refused for that reason (they would go into the host's auth file).
- Only TCP ports are published. Port ranges are not.
- Anonymous volumes (a `VOLUME` in an image, or `-v /data`) are podman's own
  and go with their container; named ones are the owner's and persist.
- `docker cp`, `docker exec`, `logs -f`, `attach` and `stats` work; `docker
  attach` over websocket does not. `docker system prune` and `docker image
  prune` are not served, as images are the host's.
- The Docker API version podman serves is whatever that podman offers; a
  `docker` older or newer than it may complain, as with podman's socket itself.
