package currentauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func fleetMigrationRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "database", "migrations")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repository root not found")
		}
		root = parent
	}
}

func fleetFunctionSourceBodies(t *testing.T, contract fleetSchemaContract) map[string]string {
	t.Helper()
	paths := fleetMigrationPaths(t)
	pattern := regexp.MustCompile(`(?is)CREATE(?: OR REPLACE)? FUNCTION (\w+)\([^)]*\)\s+RETURNS\s+\w+\s+LANGUAGE\s+\w+.*?AS \$\$(.*?)\$\$`)
	bodies := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			want, ok := contract.Functions[match[1]]
			if !ok {
				continue
			}
			body := match[2]
			if match[1] == "loomplane_state_event_trigger" {
				// planEventFunctions applies this source-owned replacement at migration.
				body = strings.ReplaceAll(body, "'summary','{}'::jsonb)", "'summary','{}'::jsonb,'leftovers','[]'::jsonb,'artifacts','[]'::jsonb)")
			}
			hash := sha256.Sum256([]byte(body))
			if hex.EncodeToString(hash[:]) == want.Body {
				bodies[match[1]] = body
			}
		}
	}
	if len(bodies) != len(contract.Functions) {
		for name := range contract.Functions {
			if _, ok := bodies[name]; !ok {
				t.Errorf("no source body matches %s", name)
			}
		}
		t.FailNow()
	}
	return bodies
}

func TestFleetSchemaFiniteContract(t *testing.T) {
	contract, err := fleetContract()
	if err != nil {
		t.Fatal(err)
	}
	bodies := fleetFunctionSourceBodies(t, contract)
	fixture := func() fleetCatalog {
		shape := ordinarySchema{Columns: map[string]map[string]schemaColumn{}, Indexes: contract.Indexes}
		for table, columns := range contract.Columns {
			shape.Columns[table] = map[string]schemaColumn{}
			for name, column := range columns {
				shape.Columns[table][name] = column
			}
		}
		for _, fk := range contract.ForeignKeys {
			fk.TargetSchema = "qualified"
			fk.Valid = true
			shape.ForeignKeys = append(shape.ForeignKeys, fk)
		}
		p := fleetCatalog{Shape: shape, CatalogFirst: true}
		for table, checks := range contract.Checks {
			for name, definition := range checks {
				p.Checks = append(p.Checks, fleetCheck{Table: table, Name: name, Definition: definition, Valid: true, Local: true})
			}
		}
		for name := range contract.Columns {
			p.Relations = append(p.Relations, fleetRelation{tableProfile: tableProfile{Name: name, OID: 42, Resolved: 42, Kind: "r", Readable: true}, Owner: 7})
		}
		for _, trigger := range contract.Triggers {
			trigger.FunctionSchema = "qualified"
			trigger.Enabled = "O"
			trigger.Columns = []string{}
			p.Triggers = append(p.Triggers, trigger)
		}
		for name, f := range contract.Functions {
			p.Functions = append(p.Functions, projectFunction{Name: name, Schema: "qualified", Owner: 7, Language: f.Language, Arguments: f.Arguments, Names: f.Names, Return: f.Return, Body: bodies[name], Volatility: f.Volatility, Parallel: "u", Kind: "f"})
		}
		return p
	}
	if err := fixture().validateFleet("qualified", 7, contract); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*fleetCatalog)
	}{
		{"missing relation", func(p *fleetCatalog) { p.Relations = p.Relations[1:] }},
		{"duplicate relation", func(p *fleetCatalog) { p.Relations[0] = p.Relations[1] }},
		{"shadow table", func(p *fleetCatalog) { p.Relations[0].Resolved++ }},
		{"foreign owner", func(p *fleetCatalog) { p.Relations[0].Owner++ }},
		{"rls", func(p *fleetCatalog) { p.Relations[0].RLS = true }},
		{"rewrite rule", func(p *fleetCatalog) { p.Relations[0].Rules = 1 }},
		{"inheritance", func(p *fleetCatalog) { p.Relations[0].Inheritance = 1 }},
		{"view", func(p *fleetCatalog) { p.Relations[0].Kind = "v" }},
		{"custom column type", func(p *fleetCatalog) { p.Relations[0].UnsafeTypes = true }},
		{"missing scope column", func(p *fleetCatalog) { delete(p.Shape.Columns["loomplane_fleet_grants"], "credential_id") }},
		{"nullable expiry", func(p *fleetCatalog) {
			p.Shape.Columns["loomplane_fleet_grants"]["expires_at"] = schemaColumn{Type: "timestamp with time zone"}
		}},
		{"timestamp without timezone", func(p *fleetCatalog) {
			p.Shape.Columns["loomplane_fleet_grants"]["expires_at"] = schemaColumn{Type: "timestamp without time zone", Required: true}
		}},
		{"missing check", func(p *fleetCatalog) { p.Checks = nil }},
		{"weakened nanosecond check", func(p *fleetCatalog) {
			for i := range p.Checks {
				if p.Checks[i].Name == "fleet_namespace_attempt_time" {
					p.Checks[i].Definition = "CHECK (isfinite(authorized_at))"
				}
			}
		}},
		{"unvalidated check", func(p *fleetCatalog) {
			for i := range p.Checks {
				p.Checks[i].Valid = false
			}
		}},
		{"inherited check", func(p *fleetCatalog) {
			for i := range p.Checks {
				p.Checks[i].Inherited = 1
			}
		}},
		{"missing uniqueness", func(p *fleetCatalog) { p.Shape.Indexes = nil }},
		{"missing foreign key", func(p *fleetCatalog) { p.Shape.ForeignKeys = nil }},
		{"deferred foreign key", func(p *fleetCatalog) {
			for i := range p.Shape.ForeignKeys {
				p.Shape.ForeignKeys[i].Deferred = true
			}
		}},
		{"cross-schema foreign key", func(p *fleetCatalog) {
			for i := range p.Shape.ForeignKeys {
				p.Shape.ForeignKeys[i].TargetSchema = "other"
			}
		}},
		{"unknown insert hook", func(p *fleetCatalog) { p.Triggers = append(p.Triggers, p.Triggers[0]) }},
		{"missing guard", func(p *fleetCatalog) { p.Triggers = p.Triggers[1:] }},
		{"changed event", func(p *fleetCatalog) { p.Triggers[0].Type = 19 }},
		{"disabled guard", func(p *fleetCatalog) { p.Triggers[0].Enabled = "D" }},
		{"conditional guard", func(p *fleetCatalog) { p.Triggers[0].Extras = true }},
		{"changed binding", func(p *fleetCatalog) { p.Triggers[0].Function = "other" }},
		{"changed body", func(p *fleetCatalog) { p.Functions[0].Body += " " }},
		{"security definer", func(p *fleetCatalog) { p.Functions[0].Security = true }},
		{"search path override", func(p *fleetCatalog) { p.Functions[0].Config = []string{"search_path=public"} }},
		{"wrong language", func(p *fleetCatalog) { p.Functions[0].Language = "sql"; p.Functions[0].Body = "SELECT true" }},
		{"function shadow", func(p *fleetCatalog) { p.Functions[0].Extras = true }},
		{"unexpected expression function", func(p *fleetCatalog) { p.UnsafeExpressions = 1 }},
		{"wrong search path order", func(p *fleetCatalog) { p.CatalogFirst = false }},
		{"operator shadow", func(p *fleetCatalog) { p.ShadowOperators = 1 }},
		{"builtin shadow", func(p *fleetCatalog) { p.ShadowFunctions = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixture()
			tc.change(&p)
			if err := p.validateFleet("qualified", 7, contract); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("qualification=%v", err)
			}
		})
	}
	connections := contract.Columns["loomplane_cloud_connections"]
	if _, ok := connections["kind"]; ok {
		t.Fatal("notification kind is not a cloud connection column")
	}
	for _, name := range []string{"generation", "method", "signed_in_by"} {
		if _, ok := connections[name]; !ok {
			t.Fatalf("publication connection column missing: %s", name)
		}
	}
	if len(contract.Checks["loomplane_fleet_namespace_attempts"]) != 7 || len(contract.Checks["loomplane_fleet_namespaces"]) != 1 {
		t.Fatal("incomplete exact namespace checks")
	}
	// The contract is finite: only schema-version reads are required for these tables.
	for _, table := range []string{"loomplane_cluster_demand_cohorts", "loomplane_cluster_pool_growth", "loomplane_cluster_pool_advice", "loomplane_cluster_capacity_inventory", "loomplane_cluster_workload_observed_accounting"} {
		if len(contract.Columns[table]) != 3 || contract.WriteEvents[table] != 0 {
			t.Fatalf("capacity contract expanded: %s", table)
		}
	}
}

func TestFleetSchemaMigrationSourceContract(t *testing.T) {
	contract, err := fleetContract()
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(fleetSchemaSourceJSON)
	want := map[string]string{}
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	paths := fleetMigrationPaths(t)
	got := map[string]string{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		relevant := false
		for name := range contract.Columns {
			if strings.Contains(string(data), name) {
				relevant = true
				break
			}
		}
		for name := range contract.Functions {
			if strings.Contains(string(data), name) {
				relevant = true
				break
			}
		}
		if relevant {
			sum := sha256.Sum256(data)
			got[filepath.Base(path)] = hex.EncodeToString(sum[:])
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("Fleet schema migration references changed; requalify the finite namespace contract")
	}
}

func fleetMigrationPaths(t *testing.T) []string {
	t.Helper()
	directories := []string{filepath.Join(fleetMigrationRoot(t), "database", "migrations")}
	if extra := os.Getenv("LOOMPLANE_AUTHORITY_MIGRATION_SOURCE_DIR"); extra != "" {
		directories = append(directories, extra)
	}
	var paths []string
	for _, directory := range directories {
		found, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, found...)
	}
	return paths
}

func TestFleetSchemaRequiresQualifiedJournal(t *testing.T) {
	contract, err := fleetContract()
	if err != nil {
		t.Fatal(err)
	}
	if contract.NamespaceEvidenceVersion == 1 {
		return
	}
	query := func(context.Context, string, ...any) profileScanner {
		t.Fatal("unqualified journal reached SQL")
		return nil
	}
	if err := inspectFleetSchema(t.Context(), query, "qualified", 7); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("qualification=%v", err)
	}
}

const fleetSchemaSourceJSON = `{
  "20261011000001_create_projects.go": "c1c7141382e7daf0a23d1292c950456554b692144825c9bc8bbde1f723303c65",
  "20261012000003_create_variables.go": "b31b5281181cc2499c12ed77d46a3619c61613506e9d6b02feb31187d171ddb4",
  "20261012000005_create_planning.go": "b514e38e9b0c6564124e3c9322e6aa522066c7fa5486b0a7edc02e498058dc25",
  "20261012000006_create_cloud_connections.go": "bc7117e821f27a9cdb39cb6056b65beafd671ef9dfc46a8faa2a3992270e2acc",
  "20261012000007_create_environment_connections.go": "9791c305a1ed19e470f11b0445fe3e6ea24baf156b080e40bb395b446ca5f5ca",
  "20261012000008_create_deployments.go": "c30a72ff2eb6788c1c3046afce313ba056595dd22f9cd75b949b261d39844f47",
  "20261014000002_create_plan_sources.go": "43669173d34625e334dadceb9e7e630719569eef067ac78b806a95d7d76465bf",
  "20261016000002_protect_connection_uses.go": "7833a7039e74922fa9a3d05d1ff893a1ff5a987fb0bf0eea4fb1bf27994e13d6",
  "20261016000003_persist_realtime_events.go": "a4801b5c5f2c12a2cf8f1d8f13d39bd7f7cbd6b09532da269406c31b1708291f",
  "20261016000006_reserve_owned_target_ports.go": "3c86e50d70869daeae7bb092e3a3c6704390e890625c4dd2736139a6f5289d4d",
  "20261018000100_create_federation_destinations.go": "74009189a63ff3785bfe2c757bec9c63052d515b049727bf41aae1a1288212c5",
  "20261018000201_service_recovery.go": "de9a5139a2154a25182dcce81643168e460c7acb7a1d4978a38ea398147239a4",
  "20261018000203_backup_policies.go": "032034d1d46126cac34bab28f60e62c564e5f88214486514b828e97f8c753eed",
  "20261018000400_create_cloud_profiles.go": "25011034449b42a508b4eb8225e1c2bfc44b5ebb6eac0871aa85d1fb73edb5d9",
  "20261018000401_cloud_sync.go": "24003181c4cea4a13e059097766251452ef041807e899b5d6bdb9ce336f5c96d",
  "20261020000300_federation_access.go": "37ba1d334d4c05655afae63db8330a713f806296371d8b9ddcb8a03b8c81b17d",
  "20261020000401_cloud_events.go": "bd20d48866dd603b370e209ef08ed5ac3c8e52c654a4db1398701d0fbfba69dd",
  "20261020000900_shared_clouds.go": "1ae205b6d59bc049fb54ad71bcd2946be8c5313f6f2c4af4c50fd417575e5d94",
  "20261024000100_cloud_infrastructure.go": "57a6aa0b3922afbb3fe5be2c9319e6ff5db5855f7fbb2396322370d710c4a186",
  "20261024000200_cloud_infrastructure_review_guards.go": "9b650b441097b4f290150c762bfd47a87c45c791c307bcd2034869ef23689a2a",
  "20261024000400_native_sources.go": "8add06aa3e68928f609cca397b219dfa04d423fd4889368fff0487a1ada71612",
  "20261025000100_server_pools.go": "b27c42da41f94daf5a21d1ef68c88cc8b43a9ad6fef95d0197094467e7c38017",
  "20261025000200_workload_controllers.go": "3d2afaff447b65221cb6d81249759a042a372f963f2e591fb0079a894ba5e3f3",
  "20261026000100_cloud_domains.go": "9b8f1e0dc2996f71ce2a92cb90ca65379d3263a22d3e5e4ff0d12ab09cb333e2",
  "20261028000100_shared_sync_admission.go": "557c5024751dec16e064d773c626b59a57b9ee35ea77067af1f0ac45d7259c07",
  "20261029000400_cloud_tags.go": "41685db4e511b433e7960f8997908d8de0210f31d0142a850275a674f0a45b2b",
  "20261029000500_cloud_default_switch.go": "83fd615072dff33d5d8134929ce6e78d567de8fc6617885349a5964bb0a6085c",
  "20261030000300_connection_use_admission.go": "16ad5b76c3515d1059724345882ffe394457a0ba4ed9fbe5813f3edeea36248b",
  "20261031000900_db_gateways.go": "c39f10da0509ab2cbf8cb4f771cd9967aa15d33c914e3b7c363e25cca1c932a9",
  "20261101000400_cloud_capacity.go": "03d9c3c1faa01231b75f3d11b06e45e44a2fe9b8c572ef236a681ea6ffa0c9b8",
  "20261101000600_cloud_capacity_refresh.go": "6ed4c1e30e207312ec7080ac895e6744095a188d5734abc52da48a474e33024a",
  "20261101000900_cloud_capacity_intents.go": "2d037f8d3dc61b03a3aef79668a1ccc86ac1ff3d163f851ed17e505a6c06b91d",
  "20261101001200_frontend_capacity.go": "35d543aba5dd30506c9d3a517b3d8fe02e939efbe1368daca2d82ba9ab5b0727",
  "20261102000300_native_retention_policy_connections.go": "ea6572409feba1f341c2ee29166fef646eaf8e86809f25d8d6b912fdfe50e0cb",
  "20261103000100_cloud_sign_in.go": "e8680744da4c00ed58a4f037cbb9b385898b704ea2e3555884df24b060f4b73e",
  "20261103000300_cloud_sign_in_people.go": "114675e53296fefe9b5ea5ad7bda98db7ca9e79669718567ff8296fa5eab361a",
  "20261104000100_canary_releases.go": "d7b00dc9b25bd0dbe7af198537adc80f409f969822e8148c27034596957f06ea",
  "20261104000200_plan_promotion_rollback.go": "038048bac4e9c5cdbe4d65e90ed53e44a93e52a2f4482e79927638aa620edb87",
  "20261104000300_release_watches.go": "06f1a48a45e266d1510cb28bfa8fc06aad2f96b692716fa9719809970c35c8fe",
  "20261105000300_kubernetes_routes.go": "29ab107afce6ca34a44a79f85ed28e8dbdefd069c89c7d98a04bdcbb546782a0",
  "20261105000400_zone_challenge_uses.go": "afffd9537564e0c38eb39c6c1541e3c2e396f2b813964d6040b8826c9e0785dd",
  "20261116000100_gpu_capacity.go": "de31e8c6cfb562a3a08dfeeaf243970c5964b3d9879a21a50ed404c1b948e4c8",
  "20261120000100_cloud_capacity_events.go": "68067c5986bd23f285358bbc8ebe1245a71123c8228ebb620885dc966e43a2c5",
  "20261126000400_managed_clusters.go": "1f573bab6c2aa0229df0e533e0fc20c44624411f4a6115b5f74bbbe1c47e89cb",
  "20261126000500_cluster_recovery.go": "910454cfaf8f3fbfd9a7df3385ed6daa5cf96ddf88c6a3c3289244904a3eb6dd",
  "20261126000600_cluster_enrollment.go": "a162691168be22bdadbd3c10e27f9203df6e2c4103c28d48d421dcd1162e2349",
  "20261126000700_cluster_maintenance_admission.go": "40b674d2ce436c6240617e665cf4a63ecf0bfaf49e6e6cb786c5b69571b3b586",
  "20261126000800_cluster_maintenance_actions.go": "be2712652c5a6a98f0e1ff957e59eb492526b1fa2e6406bd515fad17d4cc1c93",
  "20261126000900_cluster_retirement_receipts.go": "799fd18effb0b31e5a8b007007f2dddb4b6305b1a9d6f76611d121e57f99c8ae",
  "20261126001000_cluster_retirement_admission.go": "fc8caee82a26bfdbcf47221967a0a0fd67c0433137bcabe1f235abd56db489ea",
  "20261127000200_cluster_capacity_inventory.go": "2b4b38c49de2eb16182e5484668e19e01f7f43a4063b46eb21b86ccc26fc816c",
  "20261127000300_cluster_capacity_envelopes.go": "d34137994c615b160c0c93355d8438373f439b0c68ce9f5c088e0b6cdbf2361a",
  "20261127000400_cluster_workload_holds.go": "cc6b35f1a4e8c45cd555c79f62357b8869181f6dd64d70d46f6a656615e46e2c",
  "20261127000500_cluster_budget_prices.go": "6e7b7347db383751134e661b9cbbace5f5aef0b274775229951e1c6691d551c9",
  "20261128000200_cluster_workload_intents.go": "14e1192ab0378fccb343326552926c2dfe7ebbc68be538b4b144a67c2453f90f",
  "20261128000500_managed_cleanup_intents.go": "e05257d87a0630fd92e93b5a4f653dd5fed01a729b9eedda5d0b846b652696db",
  "20261129000200_firecracker_routes.go": "cd9e59619f1df13db6fb979399c803422854bc9af5f9587a99722b2e7185efab",
  "20261129000900_platform_routes.go": "d6418fd6791039ed018d30011e5fad311811d88ca8b010f05ed4bba6baa36dcd",
  "20261130000300_managed_cleanup_target_closure.go": "2e48a17824407acd76b84bc6c7d61b576513b9635889d0228ffc26c7baf3b037",
  "20261205000200_push_outbox.go": "b6859ad80f0416d45ba36a050ceebbace96d32f235d647d33a6f18c97502e055",
  "20261205000400_release_notices.go": "feaf29e8fbadb685b96c322d5a9461b0e8f9e883c8470cc4d2cabd20a079bf72",
  "20261206000200_managed_failed_cleanup.go": "44c6252b4f5ffbeca104940d2313aa3427ce3c12ea84c1abd96b11bb86439d15",
  "20261206000300_runtime_access.go": "151a9c67e621f2fe0ff54256aa75eff1647ec6d0dc03f5e01590e7591e18a122",
  "20261207000200_managed_cleanup_observations.go": "0fbe4f5e917ef9e81c27bd577dad540793df059e8738fe65547baf2f0c59400e",
  "20261207000800_managed_profile_architecture.go": "9e4953dc0f0c4c0d99ff73aefe15c013d548f7d0654435500dc7869f56a4b9f2",
  "20261207000900_cluster_pool_growth.go": "399acd2c6fe0b2298007fd10a9a3435529717011fdc0fed83ce6cd7b58c636d7",
  "20261207001000_replica_proof.go": "a9259d8ff9cacad96d96ebc202b2ee7064db802f3bc7bda42721603614933f7b",
  "20261207001100_cluster_demand_cohorts.go": "cf2f153eea5f301b9f360a8dfda6c068010e5f0e261a263e273a31a3003dacb6",
  "20261207001300_cluster_control_metadata.go": "686b8484fdd2f3f2e275323b21082b554890b5e55ec02a15b6ed2257b2f1211f",
  "20261207001400_cluster_observation_sequence.go": "4eaf7844b2bd535ade33e0d55c4b9eda3be47e4da0a6a872b78cae960003a350",
  "20261207001500_observed_accounting_metadata.go": "26404858aa4c55ac2fc5c2ddbb83438eb9f6056e3ab734bfb6d1c99c625d71ea",
  "20261207001600_cloud_locations.go": "317667145892b7a38080bdefdd1b84a70be1637b5b5d612844b76477c5326cbe",
  "20261207001700_location_placement_policy.go": "af2f70ac2dd17e4dc99b38cc26fde450c1e384b9682bb7dffb991fcde4cbde13",
  "20261207001800_placement_groups.go": "8a78e48d67af74786fc530d052e5dcae092052250e3c54e80ef582de85edcc71",
  "20261208000100_instance_fleets.go": "47dba0f3a4c5943f40cf40c9349bcff99c070a40d721aa6aaf2c07ba483aab64",
  "20261208000200_fleet_template_releases.go": "01279eb22e257f152e8554f03650f1e4a6842f6d0edde0410faa942a684b1b65",
  "20261208000300_fleet_grants.go": "35791684364d8f01dc3d5beb71b4840f00d68ba2b9f54f359e66dd8541dc3841",
  "20261208000400_fleet_plan_inputs.go": "508f7bb4b10be9a0549dc0989892c3f62c1dffab936db380b369eeef9e1d65f5",
  "20261208000600_variable_versions.go": "8875593d1abf428f598da6dbabfc137a62a63fccd117dcd5b51ad7f42b8455e7",
  "20261208000800_fleet_isolation.go": "3bf938460a0666961f95c6bb8546efc39dec4e7bb632f5f06c8b991f98775fb7",
  "20261216000200_node_events.go": "e17291be3cc68280085a3b35ff7d1ab99265c87ac5940b712a0c9a7088f74da2",
  "20261216000600_node_public_address.go": "56a1856d4c362a8227ed798ecd7a0d4aefd30cbb84686969d3b86c64ce8c1c99",
  "20261220000200_agent_foundation.go": "47d2bd061b410f43bcfc721e20ed763fb6bce62c88c1889b96af4c7a24ddae4f",
  "20261220000600_agent_reports.go": "02e431efff63ae45bad478465d9db5752a72e6ee920109bb45bee05445916d09",
  "20261220000700_release_reports.go": "59d526f065279a5aca569b8703cbe0991628bec2763a32bd04c671a14b4a4391",
  "20261221000100_platform_oauth_apps.go": "4d855e79a8edc0c8e4722dabf98d85919425dfd1c8ae93d2008bcb169eae7fe9",
  "20261223000100_platform_cloud.go": "89d8820a1ad4af227b7934438101020493664c9585130260643944bdecbe8341",
  "20261223000200_platform_cloud_routes.go": "3e5548b511224e22eff991cb68bbe3bfd419554c11ec94ae661389ce12597f31",
  "20261223001000_fleet_namespace_attempts.go": "99ee1e43cd062a267afb69cd9ba5016b69df07e3757d003b7db8ea19f093271a",
  "20261225000500_provider_environments.go": "2d94baf8102cd013ba1cc31d915bbb046378098f8bc812ff85f28cb5b2726760"
}`
