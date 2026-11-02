//go:build race

package main

// raceEnabled says this test binary was built with -race, so the gateway
// build asks for it too and reuses the packages the race suite has compiled.
const raceEnabled = true
