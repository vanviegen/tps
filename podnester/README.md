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
- A bind mount source is a path *in that container*: under one of its own bind
  mounts, or in its root filesystem (which podman keeps mounted on the host).
  It is translated to the host path naming the same file, with symlinks
  followed inside the container's view, so a link to `/home/user/.ssh` means
  the container's own `/home`, not the host's.
- Published ports are published *in that container*, the way a mount source is
  found there: the proxy runs a small forwarder inside it for each, so
  `-p 5432:5432` makes `localhost:5432` work exactly as it would on a host.
  `-p 0:5432` picks a free port and `docker port` tells which.
- Sub-containers join a bridge network of the top-level container's, which
  it is on as well, so they reach each other and it reaches them by name;
  `docker network create` makes further networks it joins too (what compose
  does). The user namespace mapping and SELinux settings of the top-level
  container are forced on them, so shared files show the same owners.
- Everything that would reach past the container is refused: privileges,
  capabilities, devices, sysctls, the host's namespaces, registry logins,
  image removal (images are the host's, shared by all), host paths in volume
  and log drivers, mount propagation into the host, and every endpoint and
  every field of a container spec the proxy does not know. Podman's own
  (libpod) API is not served; the docker CLI is the client, and `podman`
  with `CONTAINER_HOST` set gets a clear refusal.
- Sub-containers are removed when the top-level container stops, like the
  processes in it. Its volumes and networks stay for its next run; `purge`
  removes those too.

## Running

```sh
podnester run --name mytask --userns keep-id:uid=1000,gid=1000 --security-opt label=disable \
    -- --rm -it -v "$PWD:/work" -w /work myimage bash
```

`podnester run` starts a `podman run` with the arguments after `--`, adding
`--name`, `--network mytask-bridge`, the mount of the shared directory that
holds the socket and `DOCKER_HOST`/`CONTAINER_HOST` pointing at it. Give
`--userns` and `--security-opt` here what the container itself runs with, in
the same way (pass them again after `--`). Put a static `docker` in the image
or on a mount; `DOCKER_BUILDKIT=0` is set, as podman builds without buildkit.

`podnester serve --name X` serves the socket for a container run by something
else, which must be started with `--network X-bridge`, the control directory
(default `$XDG_RUNTIME_DIR/podnester/X`) mounted at `/run/podnester`, and
`DOCKER_HOST=unix:///run/podnester/podman.sock`. `podnester purge --name X
[--volumes]` removes what X made.

Podman's API is expected on `$XDG_RUNTIME_DIR/podman/podman.sock`; when
nothing answers there, a `podman system service` is started on it.

## As a library

```go
p, err := podnester.New(podnester.Config{
    Upstream: sock, Owner: name, Control: dir, ControlMount: "/run/podnester",
    UsernsMode: "keep-id:uid=1000,gid=1000", SecurityOpt: []string{"label=disable"},
})
p.EnsureNetwork(ctx)              // then run the owner with --network p.Network()
go p.ListenAndServe(ctx)          // p.Socket() on the host, p.SocketMount() inside
p.Purge(ctx, true)                // when the owner is gone for good
```

The binary embedding the proxy must call `podnester.Subcommand(os.Args)`
first thing in `main` and be built static (`CGO_ENABLED=0`): a copy of it is
placed in the control directory to run as the port forwarder inside the
container, and it is run under `podman unshare` to resolve paths in the
container's root filesystem, which rootless podman keeps in a mount namespace
of its own.

## Caveats

- Resolving a bind source and podman mounting it are two steps; a container
  replacing a directory with a symlink in between could point podman at a
  host path. The host is the same user's, so what is at stake is that user's
  files; do not serve the socket to code you would not run as yourself
  without a sandbox... which is the reason podnester exists, so be aware.
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
