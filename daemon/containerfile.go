package daemon

import (
	"regexp"
	"strings"
)

// Generation of a project's initial Containerfile.dev: a Debian base plus the
// toolchains picked for the project. TPS needs nothing from the image itself;
// its tools are mounted in at run time (see toolbox.go).

type ToolOption struct {
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	detect *regexp.Regexp // matched against root-level file names of the default branch
	layer  string
}

const aptLayer = "RUN apt-get update && apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*"

var ToolOptions = []ToolOption{
	{ID: "node", Label: "Node.js 22 (npm, plus pnpm/yarn via corepack)",
		detect: regexp.MustCompile(`^(package\.json|pnpm-lock\.yaml|yarn\.lock|\.yarnrc\.yml)$`),
		layer: "RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \\\n" +
			"    && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/* \\\n" +
			"    && corepack enable"},
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
	return `# Dev container image for this project (Containerfile.dev).
#
# TPS builds it with the task's repo clone as the (only) build context, and
# runs the task in it as uid 1000 with that clone mounted at /work. code-server
# and claude are mounted in at run time, so nothing here is TPS-specific: use
# whatever base suits the project, as long as it has bash and git, and a user
# with uid 1000 who owns a home directory. Anything the task serves should
# listen on $PORT.

FROM docker.io/library/debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git sudo bash procps psmisc ripgrep less nano \
      openssh-client unzip zip xz-utils \
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/dev

` + tools + `USER dev
WORKDIR /work
`
}
