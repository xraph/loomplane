package deployments

import "github.com/xraph/loomplane/internal/planning"

// packPlan says how the buildpack builds a service: the directory to build
// from, relative to the source root ("." or the service's own directory), and
// the KEY=VALUE variables to hand the builder.
func packPlan(cfg *planning.ServiceConfig) (path string, env []string) {
	path = "."
	switch {
	case cfg.Ecosystem == "go":
		// The Go buildpack builds one main package of a module: name it.
		env = append(env, "GOOGLE_BUILDABLE=./"+cfg.Source)
	case cfg.Ecosystem == "jvm" && cfg.Source != ".":
		// A module of a Maven or Gradle build needs the build around it,
		// so the Java buildpack builds from the root and is told which.
		env = append(env, "GOOGLE_BUILDABLE="+cfg.Source)
	default:
		path = cfg.Source
	}
	if cfg.Ecosystem == "python" && cfg.Start != "" {
		// A project can run several processes; say which this one is.
		env = append(env, "GOOGLE_ENTRYPOINT="+cfg.Start)
	}
	return path, env
}
