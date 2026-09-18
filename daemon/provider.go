package daemon

import "strings"

// A task runs on one of the agent CLIs TPS knows: claude, or pi. They differ
// in everything below the neck — how the process is started, what its event
// stream looks like, where its login and its session transcripts live, which
// models it offers — and in nothing above it: a session takes messages and
// answers in chat entries and turn ends (see agent.go). Provider is that
// seam, so that the rest of the daemon says "the agent" and means either.
//
// Which one a task runs is part of its model setting, which names both:
// "claude: sonnet", "pi: z-ai/glm-5.3-flash". The dashboard offers every
// provider's models in the one list (see refreshModels).
type Provider interface {
	// Name leads this provider's models in that list, and is the directory a
	// task keeps its state for this agent in.
	Name() string
	// Version is the release Install fetches; it is part of the toolbox's key,
	// so bumping it gets every host the new one (see toolbox.go).
	Version() string
	// Install puts the CLI in a host's toolbox, as bin/<name>.
	Install(dir string) error
	// Mounts: the podman arguments that give a container the agent's state
	// directory (dir, which is the task's own) and the login it runs on.
	Mounts(dir string) []string
	// Transcripts are the agent's session files inside that directory, as a
	// glob. They are append-only, so a save point is a length in each of them
	// (see mark.go).
	Transcripts() string
	// Models this host's CLI offers, without the "default" the list leads with.
	Models() ([]string, error)
	// Title names a task from its description, in one line, asked of this
	// host's CLI. Empty when it cannot be asked or says nothing usable.
	Title(description string) string
	// Window is what a model's context window holds before a word is said in
	// it, and where the conversation stops growing (see context.go). Asked in
	// a task's container, which is where the agent runs.
	Window(c *Container, model, system string) (ContextWindow, bool)
	// Start a session in a task's container: the process, and the events it
	// writes turned into what SessionOpts asks for.
	Start(opts SessionOpts) (Session, error)
}

// providers: every agent TPS can run. The first is the one a model that names
// no provider belongs to.
var providers = []Provider{claudeCLI{}, piCLI{}}

// defaultModel leaves the choice of model to the CLI's own configuration, and
// is what a provider is given none.
const defaultModel = "default"

// DefaultModel is the model a new task starts with.
var DefaultModel = modelName(providers[0], defaultModel)

// splitModel reads a model as a task's settings hold it — "pi:
// anthropic/claude-sonnet-5" — as the provider it names and the model to ask
// that provider for. A name that points at no provider we have is claude's.
func splitModel(name string) (Provider, string) {
	if prefix, model, ok := strings.Cut(name, ": "); ok {
		for _, p := range providers {
			if p.Name() == prefix {
				return p, model
			}
		}
	}
	return providers[0], name
}

// modelName is how a provider's model is spelled in the list and in a task's
// settings.
func modelName(p Provider, model string) string { return p.Name() + ": " + model }
