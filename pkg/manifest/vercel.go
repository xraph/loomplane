package manifest

import "strings"

// BuildCommands overrides provider defaults. Nil inherits the repository's
// setting; an empty string deliberately skips that step.
type BuildCommands struct {
	Install *string `json:"install,omitempty" yaml:"install,omitempty"`
	Build   *string `json:"build,omitempty" yaml:"build,omitempty"`
}

// HasOverrides reports whether either command is explicitly set.
func (c *BuildCommands) HasOverrides() bool { return c != nil && (c.Install != nil || c.Build != nil) }

// VercelSettings makes one manifest service own a Vercel Services deployment.
// Source still selects its application in analysis; Root selects the directory
// containing vercel.json. The repository owns routing and service definitions.
type VercelSettings struct {
	Services        bool                     `json:"services" yaml:"services"`
	Root            string                   `json:"root,omitempty" yaml:"root,omitempty"`
	ServiceCommands map[string]BuildCommands `json:"service_commands,omitempty" yaml:"service_commands,omitempty"`
}

func checkBuildCommands(add func(string, string, ...any), path string, c *BuildCommands) {
	if c == nil {
		return
	}
	for key, value := range map[string]*string{"install": c.Install, "build": c.Build} {
		if value != nil && (len(*value) > 4096 || strings.ContainsRune(*value, '\x00')) {
			add(path+"."+key, "must be at most 4096 bytes without NUL characters")
		}
	}
}

func checkVercel(add func(string, string, ...any), path string, s Settings) {
	checkBuildCommands(add, path+".commands", s.Commands)
	v := s.Vercel
	if v == nil {
		return
	}
	if v.Root != "" && !isDir(v.Root) {
		add(path+".vercel.root", "must be a directory in the repository; use . for its root")
	}
	if !v.Services && (v.Root != "" || len(v.ServiceCommands) > 0) {
		add(path+".vercel", "root and service_commands require services: true")
	}
	if v.Services && s.Commands.HasOverrides() {
		add(path+".commands", "set commands under vercel.service_commands for each Vercel service")
	}
	if len(v.ServiceCommands) > MaxServices {
		add(path+".vercel.service_commands", "too many services")
	}
	for name, commands := range v.ServiceCommands {
		if len(name) > 128 || name == "" || strings.ContainsAny(name, "/\\\x00") {
			add(path+".vercel.service_commands", "invalid service name %q", name)
		}
		checkBuildCommands(add, path+".vercel.service_commands."+name, &commands)
	}
}
