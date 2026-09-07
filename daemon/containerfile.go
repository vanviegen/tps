package daemon

import (
	"regexp"
	"strings"
)

// Generation of a project's initial Containerfile.dev. The base layers are
// byte-identical for every project, in a fixed order, so podman shares them
// across all TPS projects. Tool layers are appended in a canonical order for
// the same reason.

type ToolOption struct {
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	detect *regexp.Regexp // matched against root-level file names of the default branch
	layer  string
}

const aptLayer = "RUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*"

var ToolOptions = []ToolOption{
	{ID: "corepack", Label: "Node package managers (pnpm/yarn via corepack)",
		detect: regexp.MustCompile(`^(pnpm-lock\.yaml|yarn\.lock|\.yarnrc\.yml)$`),
		layer:  "RUN corepack enable"},
	{ID: "python", Label: "Python 3 (pip, venv)",
		detect: regexp.MustCompile(`^(requirements\.txt|pyproject\.toml|setup\.py|Pipfile)$`),
		layer:  strings.Replace(aptLayer, "%s", "python3 python3-pip python3-venv", 1)},
	{ID: "build", Label: "C/C++ build tools (gcc, make, cmake)",
		detect: regexp.MustCompile(`^(Makefile|CMakeLists\.txt|configure\.ac|meson\.build)$`),
		layer:  strings.Replace(aptLayer, "%s", "build-essential pkg-config cmake", 1)},
	{ID: "go", Label: "Go",
		detect: regexp.MustCompile(`^go\.mod$`),
		layer:  strings.Replace(aptLayer, "%s", "golang", 1)},
	{ID: "java", Label: "Java (JDK, maven)",
		detect: regexp.MustCompile(`^(pom\.xml|build\.gradle|build\.gradle\.kts)$`),
		layer:  strings.Replace(aptLayer, "%s", "default-jdk maven", 1)},
	{ID: "php", Label: "PHP (cli, composer)",
		detect: regexp.MustCompile(`^composer\.json$`),
		layer:  strings.Replace(aptLayer, "%s", "php-cli php-xml php-mbstring composer", 1)},
	{ID: "rust", Label: "Rust (rustup, cargo)",
		detect: regexp.MustCompile(`^Cargo\.toml$`),
		layer:  "USER dev\nRUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal\nENV PATH=\"/home/dev/.cargo/bin:$PATH\"\nUSER root"},
	{ID: "bun", Label: "Bun",
		detect: regexp.MustCompile(`^(bun\.lock|bun\.lockb)$`),
		layer:  "USER dev\nRUN curl -fsSL https://bun.sh/install | bash\nENV PATH=\"/home/dev/.bun/bin:$PATH\"\nUSER root"},
}

func detectTools(rootFiles []string) []string {
	ids := []string{}
	for _, t := range ToolOptions {
		for _, f := range rootFiles {
			if t.detect.MatchString(f) {
				ids = append(ids, t.ID)
				break
			}
		}
	}
	return ids
}

func generateContainerfile(toolIDs []string) string {
	var layers []string
	for _, t := range ToolOptions {
		for _, id := range toolIDs {
			if id == t.ID {
				layers = append(layers, "# --- "+t.Label+" ---\n"+t.layer)
				break
			}
		}
	}
	tools := strings.Join(layers, "\n\n")
	if tools != "" {
		tools += "\n\n"
	}
	return `# TPS dev container for this project (Containerfile.dev).
#
# Built by TPS with the task's repo clone as the (only) build context; at
# runtime that clone is mounted at /work and TPS runs code-server and the
# ` + "`claude`" + ` CLI in here, as user ` + "`dev`" + ` (uid 1000). Anything the task serves
# should listen on $PORT.
#
# Feel free to edit, but keep the base block below byte-identical to other TPS
# projects so podman can share those layers, and keep code-server, node and
# claude-code installed.

FROM docker.io/library/debian:bookworm-slim

# --- TPS base (identical across projects; add project stuff below it) ---
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git sudo bash procps psmisc ripgrep less nano \
      openssh-client unzip zip xz-utils \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/dev
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://code-server.dev/install.sh | sh \
    && npm install -g @anthropic-ai/claude-code
# --- End of TPS base ---

` + tools + `USER dev
WORKDIR /work
`
}
