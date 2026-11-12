# TPS

TPS is your all-in-one dashboard for having Claude Code work on many tasks across many projects at once. For each task you get an agent chat and a VS Code instance side-by-side, backed by its own lightweight sandbox container and repository.

https://github.com/user-attachments/assets/f22dcf8f-de0c-4b5a-8b9b-c5d39587b10a

Finished work is reviewed by an agent if you like, queued, tested and merged as one commit, and deployed with a click. Plus: steering mid-turn, services with ports in your browser, save points to revert or fork from, follow-ups on merged tasks, tasks that wait on each other, scheduled tasks, scratch tasks, remote hosts over SSH, nested containers, OpenRouter budgets for media models, and ctrl-L to get anywhere.

## Installing

Download a release into a bin directory of your own and start it:

```sh
curl -fsSL --create-dirs -o ~/.local/bin/tps https://github.com/vanviegen/tps/releases/latest/download/tps-$(uname -s | tr A-Z a-z)-$(uname -m)
chmod +x ~/.local/bin/tps && ~/.local/bin/tps --auto-start  # optionally: --auto-update
```

`--auto-start` makes TPS start at every login; the dashboard is at <http://localhost:4820/>. You may want to *install* it as a web app from your browser. To update, run the same `curl`, or add `--auto-update` and TPS installs new releases itself.

### Use it as an app

Install the dashboard as an app: in a Chromium browser, click *Install* in the address bar. TPS gets a window of its own, and VS Code gets all its keys, ctrl-w included.

### Projects hosts

Projects run on hosts: this machine, and any you add over SSH, on which TPS installs itself. A host is Linux, any distribution, with **podman** 4 or later set up rootless (the distro package normally does that) and **git**. Where this machine cannot be one, macOS for one, the dashboard says why and works with the hosts you add. WSL 2 on Windows may well do as a host (with systemd enabled in `/etc/wsl.conf`), but that is untried.

Optionally, once per host:

```sh
loginctl enable-linger
echo "$USER ALL=(root) NOPASSWD: $(command -v rtcwake)" | sudo tee /etc/sudoers.d/tps-rtcwake
echo fs.inotify.max_user_instances=4096 | sudo tee /etc/sysctl.d/90-tps-inotify.conf && sudo sysctl --system
```

- Lingering keeps a host's agents working while you are logged out or disconnected; TPS tells you when it is off.
- `rtcwake` without a password lets the host wake from suspend for scheduled tasks.
- Every task's container counts toward your user's inotify instance limit, 128 on many distributions, which file watchers in a few tasks use up.

## Building from source

With Go 1.27 and Node:

```sh
npm install && npm run build       # bundles the web UI into web/dist
CGO_ENABLED=0 go build -o tps .    # embeds it into one static binary
```

Or, without installing either, in the project's own dev image:

```sh
podman build -t tps-dev -f Containerfile.dev . && podman run --rm --userns=keep-id:uid=1000,gid=1000 -v "$PWD:/work:z" tps-dev sh -c 'npm install && npm run build && CGO_ENABLED=0 go build -o tps .'
```

`./dev.sh` rebuilds and restarts on every change, `go test ./...` runs the tests, and `./deploy.sh` tags a release on GitHub, which builds the binaries.

`demo/seed.sh` fills a home directory with two small projects and a board's worth of tasks to click through; the dev container's `app` service runs it before starting TPS. `demo/video/` makes the demo video above.

![Yeah, if you could go ahead and finish those TPS reports, that'd be great](doc/tps-reports.jpg)
