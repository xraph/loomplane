package recipes

import "slices"

func init() {
	graph := legacyFalkorDB()
	current := graph.clone()
	current.Releases[0].Revision = "2"
	current.Components[0].Readiness = &HTTPProbe{Port: "redis", Checker: "falkordb-v1"}
	current.Components[0].Files = []File{{Path: "/etc/loomplane/password"}, {Path: "/etc/loomplane/check-ready", Mode: 0o555}}
	// url remains the original host:port contract. You can use connection-url
	// when you need a Redis URL with credentials.
	current.Service = engineService("falkordb", "falkordb-rdb-v1", false)
	current.Outputs = append(current.Outputs, engineConnectionOutputs("falkordb", "FALKORDB", "redis", "6379", "default")...)
	current.Outputs = append(current.Outputs, Output{Name: "connection-url", EnvKey: "FALKORDB_CONNECTION_URL", Secret: true, Template: "redis://default:${secret:password}@${component:falkordb:address:redis}"})
	authenticatedGraph := current.clone()
	current.Releases[0].Revision = "3"
	current.Placement = PlacementServiceProject
	current.Components[0].Protection = ProtectionNative
	current.Description = "Private graph database with retained storage and native RDB backups. Recovery copies allow read-only graph queries. Keep backups outside the host."
	MustRegister(current)
	if err := Default().RegisterHistorical(authenticatedGraph); err != nil {
		panic(err)
	}
	if err := Default().RegisterHistorical(graph); err != nil {
		panic(err)
	}
	search := legacyElasticsearch()
	current = search.clone()
	current.Releases[0].Revision = "3"
	current.Components[0].Readiness = &HTTPProbe{Port: "http", Checker: "elasticsearch-v1"}
	current.Components[0].Files = []File{{Path: "/usr/share/elasticsearch/config/loomplane-probe.conf"}}
	current.Components[0].Env = slices.DeleteFunc(current.Components[0].Env, func(v EnvVar) bool {
		return v.Name == "xpack.security.authc.anonymous.roles" || v.Name == "xpack.security.authc.anonymous.authz_exception"
	})
	current.Service = engineService("elasticsearch", "elasticsearch-snapshot-v1", false)
	current.Outputs = append(current.Outputs, engineConnectionOutputs("elasticsearch", "ELASTICSEARCH", "http", "9200", "elastic")...)
	authenticatedSearch := current.clone()
	current.Releases[0].Revision = "4"
	current.Placement = PlacementServiceProject
	current.Components[0].Protection = ProtectionNative
	current.Components[0].Env = append(current.Components[0].Env, EnvVar{Name: "path.repo", Value: "/usr/share/elasticsearch/data/loomplane-snapshots"})
	current.Description = "Private authenticated search with native snapshots of user indices, mappings and aliases. Global settings and security state are excluded. Recovery access is read-only."
	MustRegister(current)
	if err := Default().RegisterHistorical(authenticatedSearch); err != nil {
		panic(err)
	}
	if err := Default().RegisterHistorical(search); err != nil {
		panic(err)
	}
	vulnerable := search.clone()
	vulnerable.Releases[0].Revision = "1"
	vulnerable.Components[0].Readiness = &HTTPProbe{Port: "http", Path: "/_cluster/health?wait_for_status=yellow&timeout=5s"}
	if err := Default().RegisterBlockedHistorical(vulnerable, "Elasticsearch 9.4 revision 1 needs a reviewed upgrade before it can run again; its anonymous probe exposes index metadata"); err != nil {
		panic(err)
	}
}

func engineService(recovery, format string, tls bool) *ServiceDefinition {
	fields := map[string]string{FieldURL: "url", FieldHost: "host", FieldHostPort: "hostport", FieldPort: "port", FieldUsername: "username", FieldPassword: "password"}
	if tls {
		fields[FieldCACert] = "ca-cert"
	}
	return &ServiceDefinition{Connection: fields, Recovery: recovery, BackupFormat: format}
}

func engineConnectionOutputs(component, prefix, port, number, username string) []Output {
	return []Output{
		{Name: "host", EnvKey: prefix + "_HOST", From: &Ref{Component: component, Field: "host:" + port}},
		{Name: "hostport", EnvKey: prefix + "_HOSTPORT", From: &Ref{Component: component, Field: "address:" + port}},
		{Name: "port", EnvKey: prefix + "_PORT", Template: number},
		{Name: "username", EnvKey: prefix + "_USERNAME", Template: username},
		{Name: "password", EnvKey: prefix + "_PASSWORD", Secret: true, Template: "${secret:password}"},
	}
}
