//go:build integration

package integration

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/providers"
	"github.com/xraph/loomplane/internal/security"
)

func TestCloudLocations(t *testing.T) {
	ctx := t.Context()
	dsn := freshDatabase(t)
	if err := ops.Migrate(ctx, dsn, migrations.All()); err != nil {
		t.Fatal(err)
	}
	db := pgdriver.New()
	if err := db.Open(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	st := cloudprofiles.NewPgStore(db)
	now := time.Now().UTC()
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','org','Project','project','ready','user',now(),now()),('project-other','org','Other','other','ready','user',now(),now())`,
	} {
		if _, err = conn.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	cs := connections.NewPgStore(db)
	c := &connections.Connection{ID: "conn", OrgID: "org", Cloud: connections.DigitalOcean, Name: "account", Account: "account", Details: map[string]string{"team_uuid": "team"}, Status: connections.StatusReady, CreatedAt: now, UpdatedAt: now}
	if err = cs.CreateConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	p := &cloudprofiles.Profile{ID: "cloud", OrgID: "org", Name: "Cloud", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = st.CreateProfile(ctx, p); err != nil {
		t.Fatal(err)
	}
	account := connections.AccountIdentity(c.Cloud, c.Account, c.Details)
	location := cloudprofiles.CloudLocation{ID: "location", OrgID: "org", CloudID: "cloud", Name: "Primary", Provider: "digitalocean", AccountID: account, ConnectionID: "conn", Region: "nyc1", NetworkRefs: []string{"vpc-private"}, CIDRs: []string{"10.0.0.0/8"}, FailureDomains: []cloudprofiles.FailureDomain{{Kind: "zone", ID: "unknown", Source: "operator", DependencyIDs: []string{"db-private"}}}, Revision: 1}
	if err = st.SaveLocation(ctx, &location, 1, 0); err != nil {
		t.Fatal(err)
	}
	duplicate := location
	duplicate.ID = "duplicate"
	duplicate.Name = " primary "
	if err = st.SaveLocation(ctx, &duplicate, 2, 0); !errors.Is(err, cloudprofiles.ErrExists) {
		t.Fatalf("duplicate alias accepted: %v", err)
	}
	other := location
	other.ID = "location-other"
	other.Name = "Other"
	// Independent endpoints can have overlapping CIDRs.
	if err = st.SaveLocation(ctx, &other, 2, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetProjectGrant(ctx, "org", "cloud", "project", nil, false); err != nil {
		t.Fatal(err)
	}
	for _, target := range []*cloudprofiles.Target{
		{ID: "legacy", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "eu-independent", DisplayName: "Legacy", Provider: "digitalocean", ConnectionID: "conn", Region: "nyc1", Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "target", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "primary", DisplayName: "Primary", Provider: "digitalocean", ConnectionID: "conn", Region: "nyc1", LocationID: "location", Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "other", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "other", DisplayName: "Other", Provider: "digitalocean", ConnectionID: "conn", Region: "nyc1", LocationID: "location-other", Revision: 1, CreatedAt: now, UpdatedAt: now},
	} {
		if err = st.CreateTarget(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = st.SetProjectGrant(ctx, "org", "cloud", "project", []string{"target", "legacy"}, false); err != nil {
		t.Fatal(err)
	}
	t.Run("scoped-read", func(t *testing.T) {
		values, listErr := st.ListLocations(ctx, "org", "project", "cloud")
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(values) != 1 || values[0].ID != "location" || values[0].AccountID != "" || values[0].ConnectionID != "" || len(values[0].NetworkRefs) != 0 || len(values[0].CIDRs) != 0 || len(values[0].FailureDomains[0].DependencyIDs) != 0 {
			t.Fatalf("grant or privacy leak: %+v", values)
		}
		for _, scope := range [][3]string{{"org", "project", "location-other"}, {"foreign", "project", "location"}, {"org", "project-other", "location"}} {
			if _, readErr := st.GetLocation(ctx, scope[0], scope[1], "cloud", scope[2]); !errors.Is(readErr, cloudprofiles.ErrNotFound) {
				t.Fatalf("location leak: %v", readErr)
			}
		}
		legacy, readErr := st.GetTarget(ctx, "org", "project", "cloud", "legacy")
		if readErr != nil || legacy.LocationID != "" {
			t.Fatalf("legacy location inferred: %+v %v", legacy, readErr)
		}
	})
	t.Run("placement-immutable", func(t *testing.T) {
		moved := location
		moved.Revision = 2
		moved.Region = "sfo3"
		if saveErr := st.SaveLocation(ctx, &moved, 3, 1); !errors.Is(saveErr, cloudprofiles.ErrConflict) {
			t.Fatalf("region changed: %v", saveErr)
		}
		if _, sqlErr := conn.Exec(ctx, `UPDATE loomplane_cloud_locations SET region='sfo3',revision=2,body=jsonb_set(jsonb_set(body,'{region}','"sfo3"'),'{revision}','2') WHERE id='location'`); sqlErr == nil {
			t.Fatal("direct SQL relocated location")
		}
		target, readErr := st.GetTarget(ctx, "org", "project", "cloud", "target")
		if readErr != nil {
			t.Fatal(readErr)
		}
		target.LocationID = "location-other"
		target.Revision++
		if updateErr := st.UpdateTarget(ctx, target, 1); !errors.Is(updateErr, cloudprofiles.ErrConflict) {
			t.Fatalf("metadata moved target: %v", updateErr)
		}
		if deleteErr := st.DeleteLocation(ctx, "org", "cloud", "location", 1, 3); !errors.Is(deleteErr, cloudprofiles.ErrInUse) {
			t.Fatalf("attached location deleted: %v", deleteErr)
		}
	})
	t.Run("resource-membership", func(t *testing.T) {
		for _, id := range []string{"cluster-matching", "cluster-wrong-region", "cluster-wrong-account"} {
			resourceAccount, region := account, "nyc1"
			if id == "cluster-wrong-region" {
				region = "sfo3"
			}
			if id == "cluster-wrong-account" {
				resourceAccount = "different-account"
			}
			if _, insertErr := conn.Exec(ctx, `INSERT INTO loomplane_managed_resources(id,org_id,cloud_id,kind,provider,account_id,connection_id,external_id,incarnation,ownership,status,revision,body) VALUES($1,'org','cloud','cluster','digitalocean',$2,'conn',$1,$1,'external','ready',1,jsonb_build_object('schema_version',1,'observation',jsonb_build_object('ref',jsonb_build_object('region',$3::text))))`, id, resourceAccount, region); insertErr != nil {
				t.Fatal(insertErr)
			}
		}
		if _, insertErr := conn.Exec(ctx, `INSERT INTO loomplane_runtime_attachments(id,org_id,cloud_id,cluster_id,project_id,namespace,namespace_uid,service_account_uid,runtime_connection_id,endpoint_fingerprint,trust_fingerprint,revision,body) VALUES('location-attachment','org','cloud','cluster-matching','project','app','ns','sa','conn','endpoint','trust',1,'{}')`); insertErr != nil {
			t.Fatal(insertErr)
		}
		wrongTarget := &cloudprofiles.Target{ID: "wrong-region-target", OrgID: "org", ProjectID: "project", CloudID: "cloud", Name: "wrong-region", DisplayName: "Wrong region", Provider: "kubernetes", ConnectionID: "conn", Region: "sfo3", LocationID: "location-other", ManagedResourceID: "cluster-matching", Revision: 1, CreatedAt: now, UpdatedAt: now}
		if targetErr := st.CreateTarget(ctx, wrongTarget); !errors.Is(targetErr, cloudprofiles.ErrInvalid) {
			t.Fatalf("managed target region differs from location: %v", targetErr)
		}
		for _, id := range []string{"cluster-wrong-region", "cluster-wrong-account"} {
			if _, insertErr := conn.Exec(ctx, `INSERT INTO loomplane_resource_relations(org_id,source_id,destination_id,kind) VALUES('org','location',$1,'membership')`, id); insertErr == nil {
				t.Fatalf("mismatched resource location accepted: %s", id)
			}
		}
		if _, insertErr := conn.Exec(ctx, `INSERT INTO loomplane_resource_relations(org_id,source_id,destination_id,kind) VALUES('org','location-other','cluster-matching','membership')`); insertErr != nil {
			t.Fatal(insertErr)
		}
		if deleteErr := st.DeleteLocation(ctx, "org", "cloud", "location-other", 1, 3); !errors.Is(deleteErr, cloudprofiles.ErrInUse) {
			t.Fatalf("resource location deleted: %v", deleteErr)
		}
	})
	t.Run("concurrent-revisions", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, name := range []string{"Renamed one", "Renamed two"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				updated := location
				updated.Name = name
				updated.Revision = 2
				results <- st.SaveLocation(ctx, &updated, 3, 1)
			}()
		}
		wg.Wait()
		close(results)
		successes, conflicts := 0, 0
		for result := range results {
			if result == nil {
				successes++
			} else if errors.Is(result, cloudprofiles.ErrConflict) {
				conflicts++
			} else {
				t.Fatal(result)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("concurrent saves: %d successes %d conflicts", successes, conflicts)
		}
	})
	t.Run("saved-attestation", func(t *testing.T) {
		service := cloudprofiles.NewService(cloudprofiles.Deps{Store: st, Authz: &infraAuth{}, Connections: &syncAccounts{store: cs}, Providers: providers.Registry()})
		current, readErr := st.GetLocation(ctx, "org", "", "cloud", "location")
		if readErr != nil {
			t.Fatal(readErr)
		}
		input := cloudprofiles.LocationVerificationInput{CloudRevision: 4, Revision: current.Revision, EvidenceRef: "audit:review-1", FailureDomains: []cloudprofiles.AttestedFailureDomain{{Kind: "zone", ID: "zone-a"}}}
		if _, verifyErr := service.VerifyLocation(ctx, security.Principal{OrgID: "org", UserID: "reader"}, "cloud", "location", input); !errors.Is(verifyErr, security.ErrForbidden) {
			t.Fatalf("unauthorized verification: %v", verifyErr)
		}
		checked, verifyErr := service.VerifyLocation(ctx, security.Principal{OrgID: "org", UserID: "manager"}, "cloud", "location", input)
		if verifyErr != nil {
			t.Fatal(verifyErr)
		}
		if checked.EvidenceSource != "operator-attested" || checked.VerifiedBy != "manager" || checked.CheckedAt.IsZero() || checked.EvidenceRevision != checked.Revision || checked.FailureDomains[0].Verified {
			t.Fatalf("verification provenance: %+v", checked)
		}
		saved, readErr := st.GetLocation(ctx, "org", "", "cloud", "location")
		if readErr != nil || saved.EvidenceRef != "audit:review-1" {
			t.Fatalf("verification not saved: %+v %v", saved, readErr)
		}
		if _, verifyErr = service.VerifyLocation(ctx, security.Principal{OrgID: "org", UserID: "manager"}, "cloud", "location", input); !errors.Is(verifyErr, cloudprofiles.ErrConflict) {
			t.Fatalf("stale verification: %v", verifyErr)
		}
		// A later metadata save clears all earlier verification facts.
		cleared, saveErr := service.SaveLocation(ctx, security.Principal{OrgID: "org", UserID: "manager"}, "cloud", cloudprofiles.LocationInput{ID: checked.ID, Name: "Changed network metadata", Provider: checked.Provider, AccountID: checked.AccountID, ConnectionID: checked.ConnectionID, Region: checked.Region, CloudRevision: 5, Revision: checked.Revision, NetworkRefs: []string{"vpc-new"}, FailureDomains: []cloudprofiles.FailureDomain{{Kind: "zone", ID: "unknown", Source: "unknown"}}})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		if cleared.EvidenceRevision != 0 {
			t.Fatal("public metadata save kept verification")
		}

		final, readErr := st.GetLocation(ctx, "org", "", "cloud", "location")
		if readErr != nil || final.EvidenceRevision != 0 || final.FailureDomains[0].Verified {
			t.Fatalf("metadata retained verification: %+v %v", final, readErr)
		}
	})
	t.Run("grant-revocation", func(t *testing.T) {
		if _, grantErr := st.SetProjectGrant(ctx, "org", "cloud", "project", nil, true); grantErr != nil {
			t.Fatal(grantErr)
		}
		if _, readErr := st.GetLocation(ctx, "org", "project", "cloud", "location"); !errors.Is(readErr, cloudprofiles.ErrNotFound) {
			t.Fatalf("revoked read: %v", readErr)
		}
		if _, listErr := st.ListLocations(ctx, "org", "project", "cloud"); !errors.Is(listErr, cloudprofiles.ErrNotFound) {
			t.Fatalf("revoked list: %v", listErr)
		}
	})
	t.Run("delete-revision", func(t *testing.T) {
		disposable := location
		disposable.ID = "disposable"
		disposable.Name = "Disposable"
		if saveErr := st.SaveLocation(ctx, &disposable, 6, 0); saveErr != nil {
			t.Fatal(saveErr)
		}
		if deleteErr := st.DeleteLocation(ctx, "org", "cloud", "disposable", 1, 6); !errors.Is(deleteErr, cloudprofiles.ErrConflict) {
			t.Fatalf("stale Cloud deletion accepted: %v", deleteErr)
		}
		if deleteErr := st.DeleteLocation(ctx, "org", "cloud", "disposable", 2, 7); !errors.Is(deleteErr, cloudprofiles.ErrConflict) {
			t.Fatalf("stale location deletion accepted: %v", deleteErr)
		}
		if deleteErr := st.DeleteLocation(ctx, "org", "cloud", "disposable", 1, 7); deleteErr != nil {
			t.Fatal(deleteErr)
		}
		if _, readErr := st.GetLocation(ctx, "org", "", "cloud", "disposable"); !errors.Is(readErr, cloudprofiles.ErrNotFound) {
			t.Fatalf("deleted location remains: %v", readErr)
		}
	})
}
