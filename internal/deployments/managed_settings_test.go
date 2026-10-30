package deployments

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func TestManagedConnectionAdvertiseHostDefaultsAndOverrides(t *testing.T) {
	for _, advertise := range []string{"", "public.example.com"} {
		for _, purpose := range []string{"runtime", "builder"} {
			t.Run(purpose+"/"+advertise, func(t *testing.T) {
				job, builder, _, _ := imageBuildFixture(t, false)
				connection, _, err := job.BuildCredentials.Credential(t.Context(), job.Deployment.OrgID, job.Action.Service.BuildTarget.Builder)
				if err != nil {
					t.Fatal(err)
				}
				if advertise == "" {
					delete(connection.Details, "advertise_host")
				} else {
					connection.Details["advertise_host"] = advertise
				}
				job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(connection.Cloud, connection.Details)
				if purpose == "builder" {
					_, _, client, sessionErr := builder.builderSession(t.Context(), job)
					if sessionErr != nil {
						t.Fatal(sessionErr)
					}
					defer client.Close()
					return
				}
				settings, _ := json.Marshal(planning.ReviewedConnectionSettings(connection.Cloud, connection.Details))
				labels := map[string]string{owned.LabelOrg: connection.OrgID, owned.LabelConnection: connection.ID, owned.LabelSettings: string(settings)}
				target := &OwnedTarget{Cloud: connections.Server, Credentials: job.BuildCredentials, ServerDial: builder.Dial}
				client, sessionErr := target.serverSession(connection.OrgID, connection.ID)(t.Context(), labels)
				if sessionErr != nil {
					t.Fatal(sessionErr)
				}
				defer client.Close()
			})
		}
	}
}

func TestManagedConnectionRejectsRecordedIdentityDrift(t *testing.T) {
	for _, key := range []string{"docker_id", "architecture"} {
		for _, value := range []string{"", "different-identity"} {
			for _, purpose := range []string{"runtime", "builder"} {
				t.Run(purpose+"/"+key+"/"+value, func(t *testing.T) {
					job, builder, _, _ := imageBuildFixture(t, false)
					connection, _, err := job.BuildCredentials.Credential(t.Context(), job.Deployment.OrgID, job.Action.Service.BuildTarget.Builder)
					if err != nil {
						t.Fatal(err)
					}
					connection.Details = maps.Clone(connection.Details)
					delete(connection.Details, "advertise_host")
					connection.Details[key] = value
					job.Action.Service.BuildTarget.BuilderSettings = planning.ReviewedBuildSettings(connection.Cloud, connection.Details)
					if purpose == "builder" {
						_, _, client, sessionErr := builder.builderSession(t.Context(), job)
						if sessionErr == nil {
							client.Close()
							t.Fatal("changed identity accepted")
						}
						return
					}
					settings, _ := json.Marshal(planning.ReviewedConnectionSettings(connection.Cloud, connection.Details))
					target := &OwnedTarget{Cloud: connections.Server, Credentials: job.BuildCredentials, ServerDial: builder.Dial}
					client, sessionErr := target.serverSession(connection.OrgID, connection.ID)(t.Context(), map[string]string{owned.LabelOrg: connection.OrgID, owned.LabelConnection: connection.ID, owned.LabelSettings: string(settings)})
					if sessionErr == nil {
						client.Close()
						t.Fatal("changed identity accepted")
					}
				})
			}
		}
	}
}

func TestManagedConnectionRejectsChangedReviewedSettings(t *testing.T) {
	for _, key := range []string{"host", "port", "user", "host_key", "docker_socket", "advertise_host"} {
		for _, purpose := range []string{"runtime", "builder"} {
			t.Run(purpose+"/"+key, func(t *testing.T) {
				job, builder, _, _ := imageBuildFixture(t, false)
				connection, _, err := job.BuildCredentials.Credential(t.Context(), job.Deployment.OrgID, job.Action.Service.BuildTarget.Builder)
				if err != nil {
					t.Fatal(err)
				}
				settings := job.Action.Service.BuildTarget.BuilderSettings
				if purpose == "runtime" {
					settings = planning.ReviewedConnectionSettings(connection.Cloud, connection.Details)
				}
				reviewed, _ := json.Marshal(settings)
				connection.Details = maps.Clone(connection.Details)
				connection.Details[key] = "changed-since-review"
				if purpose == "builder" {
					_, _, client, sessionErr := builder.builderSession(t.Context(), job)
					if sessionErr == nil {
						client.Close()
						t.Fatal("changed reviewed settings accepted")
					}
					return
				}
				target := &OwnedTarget{Cloud: connections.Server, Credentials: job.BuildCredentials, ServerDial: builder.Dial}
				client, sessionErr := target.serverSession(connection.OrgID, connection.ID)(t.Context(), map[string]string{owned.LabelOrg: connection.OrgID, owned.LabelConnection: connection.ID, owned.LabelSettings: string(reviewed)})
				if sessionErr == nil {
					client.Close()
					t.Fatal("changed reviewed settings accepted")
				}
			})
		}
	}
}
