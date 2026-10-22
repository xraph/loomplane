package migrations

import "strings"

var routedReleasesUp = func() []string {
	out := []string{serverCapacitySQL}
	for _, sql := range serverPoolsUp {
		if strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_reserve_pool_plan() ") || strings.HasPrefix(sql, "CREATE OR REPLACE FUNCTION loomplane_pool_binding_capacity() ") {
			out = append(out, sql)
		}
	}
	return out
}()
