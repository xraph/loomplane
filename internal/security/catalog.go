package security

// Permission is one thing a role may do: an action on a kind of resource.
// Warden matches it as "resource:action".
type Permission struct {
	Resource string
	Action   string
}

// Name is the permission as Warden names it.
func (p Permission) Name() string { return p.Resource + ":" + p.Action }

// The permissions Loomplane checks. Later milestones check the deployment,
// cloud account and secret ones; they're in the catalog now so a role's
// meaning doesn't change under existing members when those ship.
var (
	ProjectRead   = Permission{"project", "read"}
	ProjectCreate = Permission{"project", "create"}
	ProjectUpdate = Permission{"project", "update"}
	ProjectDelete = Permission{"project", "delete"}

	EnvironmentRead   = Permission{"environment", "read"}
	EnvironmentManage = Permission{"environment", "manage"}

	DeploymentRead    = Permission{"deployment", "read"}
	DeploymentCreate  = Permission{"deployment", "create"}
	DeploymentApprove = Permission{"deployment", "approve"}

	InfrastructureRead   = Permission{"infrastructure", "read"}
	InfrastructureModify = Permission{"infrastructure", "modify"}

	CloudAccountRead   = Permission{"cloud_account", "read"}
	CloudAccountManage = Permission{"cloud_account", "manage"}

	SecretManage = Permission{"secret", "manage"}

	MemberRead   = Permission{"member", "read"}
	MemberManage = Permission{"member", "manage"}

	OrganizationAdminister = Permission{"organization", "administer"}
)

// Role slugs. They're Warden roles in each organization's tenant.
const (
	RoleOwner     = "owner"
	RoleAdmin     = "admin"
	RoleDeveloper = "developer"
	RoleViewer    = "viewer"
)

// RoleDef is a role and what it may do.
type RoleDef struct {
	Slug        string
	Name        string
	Description string
	Permissions []Permission
}

// Catalog is the role catalog every organization gets. Each role includes
// everything the one below it may do.
func Catalog() []RoleDef {
	viewer := []Permission{
		ProjectRead, EnvironmentRead, DeploymentRead, InfrastructureRead, CloudAccountRead, MemberRead,
	}
	developer := append(clone(viewer),
		ProjectCreate, ProjectUpdate, EnvironmentManage, DeploymentCreate,
	)
	admin := append(clone(developer),
		ProjectDelete, DeploymentApprove, InfrastructureModify, CloudAccountManage, SecretManage, MemberManage,
	)
	owner := append(clone(admin), OrganizationAdminister)
	return []RoleDef{
		{Slug: RoleOwner, Name: "Owner", Description: "Everything, including the organization itself", Permissions: owner},
		{Slug: RoleAdmin, Name: "Admin", Description: "Approves deployments, manages members, cloud accounts and secrets", Permissions: admin},
		{Slug: RoleDeveloper, Name: "Developer", Description: "Creates projects and environments and requests deployments", Permissions: developer},
		{Slug: RoleViewer, Name: "Viewer", Description: "Reads everything, changes nothing", Permissions: viewer},
	}
}

// AllPermissions lists every permission any role holds, once each.
func AllPermissions() []Permission {
	seen := map[Permission]bool{}
	var out []Permission
	for _, r := range Catalog() {
		for _, p := range r.Permissions {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// RoleForMember maps an authsome organization role to the Loomplane role
// its member gets. authsome's member role is free-form beyond owner, admin
// and member; anything else reads as a viewer, so an unexpected role never
// grants more than reading.
func RoleForMember(memberRole string) string {
	switch memberRole {
	case "owner":
		return RoleOwner
	case "admin":
		return RoleAdmin
	case "member":
		return RoleDeveloper
	}
	return RoleViewer
}

func clone(ps []Permission) []Permission {
	out := make([]Permission, len(ps))
	copy(out, ps)
	return out
}
