package ctl

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestNativePITRCLIRequiresExplicitUTCAndRetainsPrecision(t *testing.T) {
	for _, input := range []string{"2026-10-22T12:00:03Z", "latest", "2026-10-22T12:00:03-05:00", "2026-10-22T12:00:03.1Z", "2026-10-22T12:00:03.123456789Z", "2026-10-22T12:00:03.1234567891Z"} {
		t.Run(input, func(t *testing.T) {
			calls := 0
			app, out := recoveryCLI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["restore_time"] != input {
					t.Error("PITR CLI changed explicit UTC time", body, err)
				}
				fmt.Fprintf(w, `{"restore_time":%q,"request_id":"pitr-request","review_token":"signed"}`, input)
			})
			err := app.Run([]string{"backups", "native-copies", "review", "source", "point", "--cloud", "cloud", "--target", "target", "--restore-time", input})
			if input == "2026-10-22T12:00:03Z" || input == "2026-10-22T12:00:03.1Z" || input == "2026-10-22T12:00:03.123456789Z" {
				if err != nil || calls != 1 || !strings.Contains(out.String(), input) {
					t.Fatal("PITR CLI did not retain time", out, err)
				}
			} else if err == nil || calls != 0 {
				t.Fatal("invalid or implicit time reached server", input, calls, err)
			}
		})
	}
}
