package migrations

import "strings"

var poolEffectFencesUp = func() []string {
	out := []string{`ALTER TABLE loomplane_pool_execution ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 0`}
	// Upgrade the approval function as well as fresh installations.
	for _, sql := range serverPoolsUp {
		if strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_reserve_pool_plan() ") {
			out = append(out, sql)
		}
	}
	return out
}()
var poolEffectFencesDown = []string{`ALTER TABLE loomplane_pool_execution DROP COLUMN IF EXISTS generation`}
