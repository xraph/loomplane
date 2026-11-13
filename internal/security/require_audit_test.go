package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// workspaceChecks are the Require calls that check at the root, with "",
// by "<file>:<function>" and how many each holds. Every one is a workspace
// action: a project grant must not reach it. A call that acts on one
// project passes that project's ID instead, so a team's grant there
// counts. A new Require(..., "") has to be added here on purpose.
var workspaceChecks = map[string]int{
	"internal/aisettings/service.go:Get":                        2,
	"internal/aisettings/service.go:Update":                     1,
	"internal/aisettings/service.go:PutProviderKey":             1,
	"internal/aisettings/service.go:DeleteProviderKey":          1,
	"internal/aisettings/huggingface.go:PutHuggingFaceToken":    1,
	"internal/aisettings/huggingface.go:DeleteHuggingFaceToken": 1,
	"internal/activity/activity.go:Summary":                     1,
	"internal/environments/service.go:ListOrganization":         1,
	"internal/environments/service.go:CreateOrganization":       1,
	"internal/environments/service.go:manage":                   1,
	"internal/projects/service.go:Create":                       1,
	"internal/projects/service.go:List":                         1,
	"internal/usage/traffic.go:Traffic":                         1,
	"internal/usage/spend.go:Spend":                             1,
	"internal/repositories/connections.go:Status":               1,
	"internal/repositories/connections.go:Connect":              1,
	"internal/repositories/connections.go:Disconnect":           1,
	"internal/repositories/connections.go:Repositories":         1,
	// Whether the workspace's GitHub installations accepted the Issues
	// permission, which AI settings shows; installations are the
	// workspace's, not a project's.
	"internal/repositories/connections.go:IssuesStatus": 1,
	// A Bitbucket workspace is connected to the whole organization, like a
	// GitHub installation, and so is a repository's push webhook.
	"internal/repositories/bitbucket_connections.go:Status":       1,
	"internal/repositories/bitbucket_connections.go:Authorize":    1,
	"internal/repositories/bitbucket_connections.go:Connect":      1,
	"internal/repositories/bitbucket_connections.go:Disconnect":   1,
	"internal/repositories/bitbucket_connections.go:Repositories": 1,
	"internal/discovery/bitbucket_webhooks.go:Enable":             1,
	// A GitLab host or account is the organization's too.
	"internal/repositories/gitlab_connections.go:AddHost":                     1,
	"internal/repositories/gitlab_connections.go:Disconnect":                  1,
	"internal/repositories/gitlab_connections.go:RemoveHost":                  1,
	"internal/repositories/gitlab_connections.go:Repositories":                1,
	"internal/repositories/gitlab_connections.go:SetWebhookSecret":            1,
	"internal/repositories/gitlab_connections.go:Status":                      1,
	"internal/repositories/gitlab_signin.go:Connect":                          1,
	"internal/repositories/gitlab_signin.go:StartSignIn":                      1,
	"internal/models/custom.go:Inspect":                                       1,
	"internal/models/browse.go:Registry":                                      1,
	"internal/models/browse.go:Entry":                                         1,
	"internal/models/browse.go:Card":                                          1,
	"internal/models/status.go:List":                                          1,
	"internal/connections/incluster.go:CreateInCluster":                       1,
	"internal/connections/service.go:Create":                                  1,
	"internal/connections/service.go:List":                                    1,
	"internal/connections/service.go:Get":                                     1,
	"internal/connections/service.go:Recheck":                                 1,
	"internal/connections/service.go:Replace":                                 1,
	"internal/connections/service.go:Delete":                                  1,
	"internal/connections/gpus.go:GPUInventory":                               1,
	"internal/connections/fence_repair.go:fenceRepair":                        1,
	"internal/connections/signin.go:StartSignIn":                              1,
	"internal/connections/signin.go:CompleteSignIn":                           1,
	"internal/connections/keyless.go:PrepareKeyless":                          1,
	"internal/connections/keyless.go:KeylessSetupFor":                         1,
	"internal/connections/keyless.go:CreateKeyless":                           1,
	"internal/connections/keyless.go:ReplaceKeyless":                          1,
	"internal/connections/node.go:CreateNodeConnection":                       1,
	"internal/connections/storage.go:storageVolumes":                          1,
	"internal/gpus/gpus.go:Targets":                                           2,
	"internal/cloudinfra/cluster_retirement.go:PreviewClusterRetirement":      1,
	"internal/cloudinfra/cluster_retirement.go:ResolveClusterRetirementInput": 1,
	"internal/cloudinfra/cluster_scaling.go:ObserveClusterScaling":            1,
	"internal/cloudinfra/cluster_capacity_store.go:ObserveClusterCapacity":    2,
	"internal/cloudinfra/cluster_drift.go:ObserveClusterDrift":                1,
	"internal/cloudinfra/clusters.go:clusterAuthority":                        3,
	"internal/cloudinfra/cluster_maintenance.go:maintenanceResource":          1,
	"internal/cloudinfra/cluster_maintenance.go:prepareMaintenance":           1,
	"internal/cloudinfra/cluster_drain.go:PreviewClusterDrain":                1,
	"internal/cloudinfra/cluster_drain.go:observeClusterManagementWith":       1,
	"internal/catalogconnect/service.go:create":                               1,
	"internal/catalogconnect/service.go:AuthorizeCheck":                       1,
	"internal/catalogconnect/service.go:List":                                 1,
	"internal/catalogconnect/service.go:Delete":                               1,
	"extensions/identity/session.go:getSession":                               1,
	"extensions/identity/members.go:require":                                  1,
	"extensions/infrastructure/extension.go:status":                           1,
	// A workspace's billing (cloud edition) is the whole workspace's.
	"extensions/billing/routes.go:allow": 1,
	// Endpoint credentials and synced provider resources are the
	// workspace's, so re-checking an approver's secret:manage on them, and
	// reading or connecting the inventory, stays at the root. Granting a
	// fleet credential is an organization administrator's.
	"internal/deployments/cloudflare_resources.go:cloudflareResolveEndpoint": 1,
	"internal/planning/edge_endpoint_refs.go:resourceEndpointPins":           1,
	"internal/planning/existing_resource_service.go:checkExistingResources":  1,
	"internal/providerresources/service.go:require":                          1,
	"internal/fleets/grants.go:grantOwner":                                   1,
	// Teams are a workspace's: reading them is member:read, changing them
	// member:manage, both at the root.
	"internal/teams/service.go:List":         1,
	"internal/teams/service.go:Get":          1,
	"internal/teams/service.go:Create":       1,
	"internal/teams/service.go:Update":       1,
	"internal/teams/service.go:Delete":       1,
	"internal/teams/service.go:AddMember":    1,
	"internal/teams/service.go:RemoveMember": 1,
	"internal/teams/service.go:SetGrant":     1,
	"internal/teams/service.go:RemoveGrant":  1,
	// The AI agent is the workspace's: its settings, provider keys and
	// budget are ai:manage at the root, and whether a person may use it at
	// all (its status, and the codec that rebuilds a run's principal) is
	// agent:use there. Each operation it runs then checks its own project.
	// Live request events reach ai:manage holders across the workspace.
	"internal/agentsettings/service.go:Get":               1,
	"internal/agentsettings/service.go:Update":            1,
	"internal/agentsettings/service.go:SetBudget":         1,
	"internal/agentsettings/service.go:PutProviderKey":    1,
	"internal/agentsettings/service.go:DeleteProviderKey": 1,
	"internal/agentsettings/service.go:Status":            1,
	"internal/agentauthz/codec.go:DecodePrincipal":        1,
	"extensions/realtime/handlers.go:agentRequestReaches": 1,
	// Reading the agent's reports is agent:use at the root, like the
	// agent's status. Each report then needs deployment:read on its own
	// project, checked with that project's ID so a team's grant counts.
	"internal/agentreports/service.go:List": 1,
	"internal/agentreports/service.go:Get":  1,
	// The watcher's status, paused for the budget or not, is agent:use
	// at the root, like the agent's own status.
	"internal/agentwatch/watcher.go:Status": 1,
	// Replaying a fleet event's dead letter re-sends its payload to a
	// receiver an operator fixed, so it takes a workspace admin
	// (member:manage at the root) as well as project:update on the fleet's
	// own project.
	"internal/fleets/delivery.go:RetryEventDelivery": 1,
}

// rootChecks walks every non-test Go file under internal and extensions
// and counts, by "<file>:<function>", the Require calls whose resource is
// the literal "" and, separately, the Allowed calls, which always check at
// the root.
func rootChecks(t *testing.T) (requires, allowed map[string]int) {
	t.Helper()
	requires, allowed = map[string]int{}, map[string]int{}
	fset := token.NewFileSet()
	for _, dir := range []string{"../../internal", "../../extensions"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, "../../"))
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := rel + ":" + fn.Name.Name
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch sel.Sel.Name {
					case "Require":
						if len(call.Args) == 4 && isEmptyString(call.Args[3]) {
							requires[key]++
						}
					case "Allowed":
						allowed[key]++
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return requires, allowed
}

func isEmptyString(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && s == ""
}

// A check that acts on one project has to pass that project, or a team's
// grant on it never counts. Only workspace actions check at the root.
func TestNoProjectActionChecksTheWholeWorkspace(t *testing.T) {
	requires, allowed := rootChecks(t)
	for _, key := range slices.Sorted(maps.Keys(requires)) {
		if want := workspaceChecks[key]; requires[key] != want {
			t.Errorf("%s has %d Require(..., \"\") calls, the allowlist %d: pass the project's ID, or add a workspace action to workspaceChecks", key, requires[key], want)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(workspaceChecks)) {
		if _, ok := requires[key]; !ok {
			t.Errorf("%s is in workspaceChecks but no longer checks at the root: take it out", key)
		}
	}
	total := 0
	for _, n := range workspaceChecks {
		total += n
	}
	if total != 105 {
		t.Errorf("workspaceChecks holds %d calls, want the brief's 63, the 9 teams calls, the 6 workspace checks main added, the agent's 8 and its GitHub issues status, the 2 that read its reports, the watcher's status, the 6 Bitbucket ones, the 8 GitLab ones, the node connection, billing's and a server's fence repair, a fleet dead letter's replay, less the 3 sign-in app calls that no longer check an organization", total)
	}

	var models []string
	for key, n := range allowed {
		if strings.HasPrefix(key, "internal/models/") {
			for range n {
				models = append(models, key)
			}
		}
	}
	if !slices.Equal(models, []string{"internal/models/server_first.go:reviewServer"}) {
		t.Errorf("Allowed calls in internal/models: %v, want only server_first.go's OrganizationCloudManage check; check one project with AllowedOn", models)
	}
}
