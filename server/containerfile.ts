/**
 * Generation of a project's initial Containerfile.dev.
 *
 * The base layers are byte-identical for every project, in a fixed order, so
 * podman shares them across all TPS projects. Tool layers are appended in a
 * canonical order for the same reason.
 */

export interface ToolOption {
	id: string;
	label: string;
	detect: RegExp; // matched against root-level file names of the default branch
	layer: string;
}

export const TOOL_OPTIONS: ToolOption[] = [
	{
		id: 'corepack', label: 'Node package managers (pnpm/yarn via corepack)',
		detect: /^(pnpm-lock\.yaml|yarn\.lock|\.yarnrc\.yml)$/,
		layer: 'RUN corepack enable',
	},
	{
		id: 'python', label: 'Python 3 (pip, venv)',
		detect: /^(requirements\.txt|pyproject\.toml|setup\.py|Pipfile)$/,
		layer: 'RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-pip python3-venv && rm -rf /var/lib/apt/lists/*',
	},
	{
		id: 'build', label: 'C/C++ build tools (gcc, make, cmake)',
		detect: /^(Makefile|CMakeLists\.txt|configure\.ac|meson\.build)$/,
		layer: 'RUN apt-get update && apt-get install -y --no-install-recommends build-essential pkg-config cmake && rm -rf /var/lib/apt/lists/*',
	},
	{
		id: 'go', label: 'Go',
		detect: /^go\.mod$/,
		layer: 'RUN apt-get update && apt-get install -y --no-install-recommends golang && rm -rf /var/lib/apt/lists/*',
	},
	{
		id: 'java', label: 'Java (JDK, maven)',
		detect: /^(pom\.xml|build\.gradle|build\.gradle\.kts)$/,
		layer: 'RUN apt-get update && apt-get install -y --no-install-recommends default-jdk maven && rm -rf /var/lib/apt/lists/*',
	},
	{
		id: 'php', label: 'PHP (cli, composer)',
		detect: /^composer\.json$/,
		layer: 'RUN apt-get update && apt-get install -y --no-install-recommends php-cli php-xml php-mbstring composer && rm -rf /var/lib/apt/lists/*',
	},
	{
		id: 'rust', label: 'Rust (rustup, cargo)',
		detect: /^Cargo\.toml$/,
		layer: 'USER dev\nRUN curl --proto \'=https\' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal\nENV PATH="/home/dev/.cargo/bin:$PATH"\nUSER root',
	},
	{
		id: 'bun', label: 'Bun',
		detect: /^(bun\.lock|bun\.lockb)$/,
		layer: 'USER dev\nRUN curl -fsSL https://bun.sh/install | bash\nENV PATH="/home/dev/.bun/bin:$PATH"\nUSER root',
	},
];

export function detectTools(rootFiles: string[]): string[] {
	return TOOL_OPTIONS.filter(t => rootFiles.some(f => t.detect.test(f))).map(t => t.id);
}

export function generateContainerfile(toolIds: string[]): string {
	const layers = TOOL_OPTIONS.filter(t => toolIds.includes(t.id))
		.map(t => `# --- ${t.label} ---\n${t.layer}`).join('\n\n');
	return `# TPS dev container for this project (Containerfile.dev).
#
# Built by TPS with the task's repo clone as the (only) build context; at
# runtime that clone is mounted at /work and TPS runs code-server and the
# \`claude\` CLI in here, as user \`dev\` (uid 1000). Anything the task serves
# should listen on $PORT.
#
# Feel free to edit, but keep the base block below byte-identical to other TPS
# projects so podman can share those layers, and keep code-server, node and
# claude-code installed.

FROM docker.io/library/debian:bookworm-slim

# --- TPS base (identical across projects; add project stuff below it) ---
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8
RUN apt-get update && apt-get install -y --no-install-recommends \\
      ca-certificates curl git sudo bash procps psmisc ripgrep less nano \\
      openssh-client unzip zip xz-utils \\
    && rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 -s /bin/bash dev && echo 'dev ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/dev
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \\
    && apt-get install -y --no-install-recommends nodejs \\
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://code-server.dev/install.sh | sh \\
    && npm install -g @anthropic-ai/claude-code
# --- End of TPS base ---

${layers ? layers + '\n\n' : ''}USER dev
WORKDIR /work
`;
}
