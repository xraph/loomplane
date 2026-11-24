package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/pkg/manifest"
)

// wiredAccess is how a service gets into a workspace service, as its links
// carry it.
type wiredAccess struct{ mode, database, user string }

// wiringAccess checks the access mode a service is wired with for need, and
// says what its links carry: the mode, the service's own database and the
// user Loomplane makes for it. The shared credentials carry nothing. A
// mode the workspace service can't give is a problem, and ok is false.
func (p *planner) wiringAccess(service, need string, w manifest.Wiring) (out wiredAccess, ok bool) {
	if !manifest.OwnAccess(w.Access) {
		return wiredAccess{}, true
	}
	path := "services." + service + ".wiring." + need + ".access"
	problem := func(format string, args ...any) {
		p.problem(Finding{Code: "wiring-access", Subject: ServiceSubject, Name: service, Path: path, Message: fmt.Sprintf(format, args...)})
	}
	endpoint := p.workspaceEndpoint(need)
	if endpoint == nil {
		problem("%s gets %s with its own database or user only when %s is a workspace service registered by its connection string. Remove wiring.%s.access.", service, need, need, need)
		return wiredAccess{}, false
	}
	out = wiredAccess{mode: w.Access, database: w.Database}
	engine := endpoint.Engine
	if out.database == "" {
		if engine == catalogconnect.EngineRedis {
			problem("Redis databases are numbered: set wiring.%s.database to the one %s uses, such as 1.", need, service)
			return wiredAccess{}, false
		}
		out.database = ownDatabaseName(service)
	}
	if engine == catalogconnect.EngineRedis {
		if n, err := strconv.Atoi(out.database); err != nil || n < 0 || n > 9999 {
			problem("Redis databases are numbered: %q isn't a database number.", out.database)
			return wiredAccess{}, false
		}
	}
	var caps *catalogconnect.Capabilities
	if endpoint.LastCheck != nil {
		caps = endpoint.LastCheck.Capabilities
		if w.Access == manifest.AccessOwnUser {
			caps = endpoint.LastCheck.Admin
		}
	}
	switch w.Access {
	case manifest.AccessOwnUser:
		if engine != catalogconnect.EnginePostgres && engine != catalogconnect.EngineMySQL {
			problem("Loomplane makes users of their own on Postgres and MySQL only. Give %s its own database on %s instead (access: own_database).", service, endpoint.Name)
			return wiredAccess{}, false
		}
		if !endpoint.AdminSet || caps == nil || !caps.CreateUser {
			problem("%s has no admin credential that can create users, so Loomplane can't make one for %s. Add one on the workspace service's page, or use access: own_database.", endpoint.Name, service)
			return wiredAccess{}, false
		}
		if !caps.CreateDatabase {
			p.warn(Finding{Code: "wiring-database-must-exist", Subject: ServiceSubject, Name: service, Path: path,
				Message: fmt.Sprintf("%s's admin credential can't create databases, so the database %s must already be on %s. Loomplane gives %s's user access to it.", endpoint.Name, out.database, endpoint.Name, service)})
		}
		out.user = OwnUserName(p.in.ProjectSlug, p.in.ProjectID, service)
	case manifest.AccessOwnDatabase:
		// MongoDB makes a database when it's first written, and a Redis
		// database is a number; only SQL servers create one.
		if (engine == catalogconnect.EnginePostgres || engine == catalogconnect.EngineMySQL) && (caps == nil || !caps.CreateDatabase) {
			p.warn(Finding{Code: "wiring-database-must-exist", Subject: ServiceSubject, Name: service, Path: path,
				Message: fmt.Sprintf("%s's credentials can't create databases, so the database %s must already be on %s.", endpoint.Name, out.database, endpoint.Name)})
		}
	}
	return out, true
}

// workspaceEndpoint is the registered workspace service a resource
// attaches, or nil when it attaches none.
func (p *planner) workspaceEndpoint(need string) *catalogconnect.Endpoint {
	dep := p.resources[need]
	if dep == nil || dep.Resource == nil || dep.Provider != WorkspaceProvider || dep.Resource.ExistingResource == nil {
		return nil
	}
	check, ok := p.in.ExistingResources[dep.Resource.ExistingResource.EndpointID]
	if !ok || !check.workspaceService() {
		return nil
	}
	return check.Endpoint
}

// ownDatabaseName is the database a service gets when it doesn't name one:
// its own name, with what a database name can't hold as underscores.
func ownDatabaseName(service string) string {
	return sqlName(service, 63)
}

// ownUserMax fits MySQL's user names, the shortest of the engines that
// get one.
const ownUserMax = 32

// OwnUserName is the user Loomplane makes for a project's service on a
// workspace service: lp_<project>_<service>, shortened with a hash when
// it's longer than every engine takes. The project's ID stands in for a
// project without a slug.
func OwnUserName(projectSlug, projectID, service string) string {
	project := projectSlug
	if project == "" {
		project = projectID[strings.LastIndex(projectID, "_")+1:]
	}
	full := sqlName("lp_"+project+"_"+service, 1<<10)
	if len(full) <= ownUserMax {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	return full[:ownUserMax-9] + "_" + hex.EncodeToString(sum[:4])
}

// sqlName is s in lower case, with anything but letters, digits and
// underscores as underscores, cut to max bytes.
func sqlName(s string, limit int) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			b[i] = '_'
		}
	}
	if len(b) > limit {
		b = b[:limit]
	}
	return string(b)
}
