package projects

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// The dashboard draws each project as a small tile: an icon on a tint of a
// color. The server owns the list of names so the API, the CLI and future
// agents all agree on what a project can wear, and every stored value is
// one the dashboard knows how to draw. The names are short ones the
// dashboard maps to lucide icons, not lucide's own, so a rename in lucide
// changes nothing here.
//
// Don't remove a name: projects already wearing it would keep it, and the
// dashboard would have nothing to draw. Add new ones at the end, so these
// lists still start with the copies migration 20261012000004 backfilled
// older projects from.
var (
	icons = []string{
		"store", "bag", "card", "receipt", "wallet", "truck", "bell", "mail",
		"chat", "megaphone", "users", "calendar", "database", "chart", "search", "sparkles",
		"bot", "radar", "server", "cpu", "webhook", "braces", "terminal", "workflow",
		"package", "layers", "smartphone", "globe", "pin", "health", "learning", "book",
		"film", "music", "image", "gamepad", "shield", "key", "fingerprint", "rocket",
		"zap", "compass", "anchor", "leaf", "flame", "mountain", "atom", "hexagon",
	}
	colors = []string{"gold", "orange", "red", "rose", "violet", "indigo", "blue", "teal", "green", "slate"}
)

// An empty icon or color is invalid too: Create picks one when the caller
// leaves it out, so a project never has nothing to draw.
func validateIcon(icon string) error {
	if !slices.Contains(icons, icon) {
		return fmt.Errorf("%w: the icon must be one of %s", ErrInvalid, strings.Join(icons, ", "))
	}
	return nil
}

func validateColor(color string) error {
	if !slices.Contains(colors, color) {
		return fmt.Errorf("%w: the color must be one of %s", ErrInvalid, strings.Join(colors, ", "))
	}
	return nil
}

// pickStyle returns in, or a random name from names when in is empty. It
// doesn't check in: the caller validates whatever comes back.
func pickStyle(in string, names []string) string {
	if in != "" {
		return in
	}
	// A tile needs variety, not unpredictability, so math/rand will do.
	return names[rand.IntN(len(names))] //nolint:gosec // G404: cosmetic, not a secret
}
