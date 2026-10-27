package config

// AIConfig switches the AI section on. It is off unless the operator asks.
type AIConfig struct {
	// Enabled turns on the AI section (ai.enabled). Later AI settings join
	// this struct rather than starting a second block.
	Enabled bool
}
