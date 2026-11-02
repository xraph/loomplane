//go:build race

package buildinfo

// raceEnabled says this test binary was built with -race, so the CLI build
// asks for it too and reuses the packages the race suite has compiled.
const raceEnabled = true
