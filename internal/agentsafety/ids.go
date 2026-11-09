package agentsafety

import (
	"regexp"
	"strconv"
	"strings"
)

// idHold starts the placeholder a held ID takes during a scan.
const idHold = "loomplane-id-"

// longID is a typeid at least 32 characters long, as in atask_01m6...: a
// prefix, an underscore and 26 characters of base32. Shield's entropy check
// takes a standalone token that long, made of lower case, digits and an
// underscore, for a secret when its characters happen to be spread widely
// enough, and it did so to an agent task's ID now and then. A typeid is a
// UUIDv7, and shield already clears UUIDs, so these are held out of the scan.
var longID = regexp.MustCompile(`\b[a-z][a-z0-9]{4,11}_[0-7][0-9a-hjkmnp-tv-z]{25}\b`)

// holdIDs swaps each long ID in text for a placeholder and returns the
// replacer that puts them back. Text that already holds a placeholder is
// returned as it is, so a restored placeholder can't be something else's.
func holdIDs(text string) (string, *strings.Replacer) {
	if strings.Contains(text, idHold) {
		return text, strings.NewReplacer()
	}
	var hold, restore []string
	held := map[string]bool{}
	for _, id := range longID.FindAllString(text, -1) {
		if held[id] {
			continue
		}
		held[id] = true
		ph := idHold + strconv.Itoa(len(held)) + "-held"
		hold, restore = append(hold, id, ph), append(restore, ph, id)
	}
	if len(hold) == 0 {
		return text, strings.NewReplacer()
	}
	return strings.NewReplacer(hold...).Replace(text), strings.NewReplacer(restore...)
}
