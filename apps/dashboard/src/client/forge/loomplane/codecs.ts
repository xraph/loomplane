// Generated codec table
//
// Describes, per schema, how a wire payload maps onto its TypeScript
// shape. `ts` is the client-side field name derived from the wire name by
// the configured FieldNaming strategy (or a FieldOverrides entry); encode
// and decode below walk this table to rename between the two.
//
// Each entry is also emitted as its own module under src/codecs/, for a
// consumer that wants one codec rather than all of them.

import type { Codec, CodecRef } from './codec-runtime';
import { decode as decodeRef, encode as encodeRef } from './codec-runtime';

export type { Codec, CodecRef } from './codec-runtime';

export const CODECS: Record<string, Codec> = {
  "Analysis": {"kind": "object", "fields": {"commit_sha": {"ts": "commitSha"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "error": {"ts": "error"}, "finished_at": {"ts": "finishedAt"}, "id": {"ts": "id"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "ref": {"ts": "ref"}, "repository": {"ts": "repository"}, "started_at": {"ts": "startedAt"}, "status": {"ts": "status"}, "summary": {"ts": "summary", "codec": () => CODECS["Summary"]}}, "required": ["commit_sha", "created_at", "created_by", "error", "id", "org_id", "project_id", "ref", "repository", "status", "summary"]},
  "AnalysisGraph": {"kind": "object", "fields": {"graph": {"ts": "graph", "codec": () => CODECS["Graph"]}, "id": {"ts": "id"}}, "required": ["graph", "id"]},
  "AnalysisList": {"kind": "object", "fields": {"analyses": {"ts": "analyses", "codec": () => CODECS["AnalysisList.analyses"]}}, "required": ["analyses"]},
  "AnalysisList.analyses": {"kind": "array", "items": () => CODECS["Analysis"]},
  "BuildSystem": {"kind": "object", "fields": {"evidence": {"ts": "evidence", "codec": () => CODECS["Evidence"]}, "name": {"ts": "name"}, "path": {"ts": "path"}}, "required": ["evidence", "name", "path"]},
  "Commands": {"kind": "object", "fields": {"build": {"ts": "build"}, "dev": {"ts": "dev"}, "start": {"ts": "start"}}},
  "ConnectGitHubInputBody": {"kind": "object", "fields": {"code": {"ts": "code"}, "installation_id": {"ts": "installationId"}, "state": {"ts": "state"}}, "required": ["code", "installation_id", "state"]},
  "ConnectedRepository": {"kind": "object", "fields": {"default_branch": {"ts": "defaultBranch"}, "full_name": {"ts": "fullName"}, "installation": {"ts": "installation"}, "private": {"ts": "private"}, "url": {"ts": "url"}}, "required": ["default_branch", "full_name", "installation", "private", "url"]},
  "ConnectedRepositoryList": {"kind": "object", "fields": {"repositories": {"ts": "repositories", "codec": () => CODECS["ConnectedRepositoryList.repositories"]}}, "required": ["repositories"]},
  "ConnectedRepositoryList.repositories": {"kind": "array", "items": () => CODECS["ConnectedRepository"]},
  "CreateEnvironmentInputBody": {"kind": "object", "fields": {"kind": {"ts": "kind"}, "name": {"ts": "name"}}, "required": ["name"]},
  "CreateInvitationInputBody": {"kind": "object", "fields": {"email": {"ts": "email"}, "role": {"ts": "role"}}, "required": ["email"]},
  "CreateProjectInputBody": {"kind": "object", "fields": {"color": {"ts": "color"}, "description": {"ts": "description"}, "icon": {"ts": "icon"}, "name": {"ts": "name"}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}}, "required": ["name"]},
  "CreateVariableInputBody": {"kind": "object", "fields": {"environment": {"ts": "environment"}, "key": {"ts": "key"}, "secret": {"ts": "secret"}, "service": {"ts": "service"}, "value": {"ts": "value"}}, "required": ["key", "value"]},
  "CreatedInvitation": {"kind": "object", "fields": {"accept_path": {"ts": "acceptPath"}, "invitation": {"ts": "invitation", "codec": () => CODECS["OrgInvitation"]}}, "required": ["accept_path", "invitation"]},
  "Edge": {"kind": "object", "fields": {"confidence": {"ts": "confidence"}, "evidence": {"ts": "evidence", "codec": () => CODECS["Edge.evidence"]}, "from": {"ts": "from"}, "kind": {"ts": "kind"}, "to": {"ts": "to"}}, "required": ["confidence", "evidence", "from", "kind", "to"]},
  "Edge.evidence": {"kind": "array", "items": () => CODECS["Evidence"]},
  "Environment": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "updated_at": {"ts": "updatedAt"}}, "required": ["created_at", "id", "kind", "name", "org_id", "project_id", "updated_at"]},
  "EnvironmentList": {"kind": "object", "fields": {"environments": {"ts": "environments", "codec": () => CODECS["EnvironmentList.environments"]}}, "required": ["environments"]},
  "EnvironmentList.environments": {"kind": "array", "items": () => CODECS["Environment"]},
  "Evidence": {"kind": "object", "fields": {"detail": {"ts": "detail"}, "line": {"ts": "line"}, "path": {"ts": "path"}}, "required": ["detail", "path"]},
  "GitHubInstallation": {"kind": "object", "fields": {"account": {"ts": "account"}, "account_type": {"ts": "accountType"}, "created_at": {"ts": "createdAt"}, "id": {"ts": "id"}}, "required": ["account", "account_type", "created_at", "id"]},
  "GitHubStatus": {"kind": "object", "fields": {"configured": {"ts": "configured"}, "install_url": {"ts": "installUrl"}, "installations": {"ts": "installations", "codec": () => CODECS["GitHubStatus.installations"]}}, "required": ["configured", "installations"]},
  "GitHubStatus.installations": {"kind": "array", "items": () => CODECS["GitHubInstallation"]},
  "Graph": {"kind": "object", "fields": {"build_systems": {"ts": "buildSystems", "codec": () => CODECS["Graph.build_systems"]}, "edges": {"ts": "edges", "codec": () => CODECS["Graph.edges"]}, "notes": {"ts": "notes", "codec": () => CODECS["Graph.notes"]}, "packages": {"ts": "packages", "codec": () => CODECS["Graph.packages"]}, "resources": {"ts": "resources", "codec": () => CODECS["Graph.resources"]}, "services": {"ts": "services", "codec": () => CODECS["Graph.services"]}, "version": {"ts": "version"}, "workspaces": {"ts": "workspaces", "codec": () => CODECS["Graph.workspaces"]}}, "required": ["build_systems", "edges", "notes", "packages", "resources", "services", "version", "workspaces"]},
  "Graph.build_systems": {"kind": "array", "items": () => CODECS["BuildSystem"]},
  "Graph.edges": {"kind": "array", "items": () => CODECS["Edge"]},
  "Graph.notes": {"kind": "array", "items": () => CODECS["Note"]},
  "Graph.packages": {"kind": "array", "items": () => CODECS["Package"]},
  "Graph.resources": {"kind": "array", "items": () => CODECS["Resource"]},
  "Graph.services": {"kind": "array", "items": () => CODECS["Service"]},
  "Graph.workspaces": {"kind": "array", "items": () => CODECS["Workspace"]},
  "InfrastructureStatus": {"kind": "object", "fields": {"engine": {"ts": "engine"}, "providers": {"ts": "providers", "codec": () => CODECS["InfrastructureStatus.providers"]}, "store": {"ts": "store"}}, "required": ["engine", "providers", "store"]},
  "InfrastructureStatus.providers": {"kind": "array", "items": () => CODECS["ProviderStatus"]},
  "Note": {"kind": "object", "fields": {"message": {"ts": "message"}, "path": {"ts": "path"}}, "required": ["message"]},
  "OrgInvitation": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "email": {"ts": "email"}, "expires_at": {"ts": "expiresAt"}, "id": {"ts": "id"}, "invited_by": {"ts": "invitedBy"}, "role": {"ts": "role"}}, "required": ["created_at", "email", "expires_at", "id", "invited_by", "role"]},
  "OrgInvitationList": {"kind": "object", "fields": {"invitations": {"ts": "invitations", "codec": () => CODECS["OrgInvitationList.invitations"]}}, "required": ["invitations"]},
  "OrgInvitationList.invitations": {"kind": "array", "items": () => CODECS["OrgInvitation"]},
  "OrgMember": {"kind": "object", "fields": {"email": {"ts": "email"}, "id": {"ts": "id"}, "joined_at": {"ts": "joinedAt"}, "member_role": {"ts": "memberRole"}, "name": {"ts": "name"}, "role": {"ts": "role"}, "user_id": {"ts": "userId"}}, "required": ["email", "id", "joined_at", "member_role", "name", "role", "user_id"]},
  "OrgMemberList": {"kind": "object", "fields": {"members": {"ts": "members", "codec": () => CODECS["OrgMemberList.members"]}}, "required": ["members"]},
  "OrgMemberList.members": {"kind": "array", "items": () => CODECS["OrgMember"]},
  "Package": {"kind": "object", "fields": {"ecosystem": {"ts": "ecosystem"}, "evidence": {"ts": "evidence", "codec": () => CODECS["Package.evidence"]}, "id": {"ts": "id"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "runtime": {"ts": "runtime"}}, "required": ["ecosystem", "evidence", "id", "name", "path"]},
  "Package.evidence": {"kind": "array", "items": () => CODECS["Evidence"]},
  "Project": {"kind": "object", "fields": {"color": {"ts": "color"}, "created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "description": {"ts": "description"}, "icon": {"ts": "icon"}, "id": {"ts": "id"}, "name": {"ts": "name"}, "org_id": {"ts": "orgId"}, "repository_url": {"ts": "repositoryUrl"}, "slug": {"ts": "slug"}, "status": {"ts": "status"}, "updated_at": {"ts": "updatedAt"}}, "required": ["color", "created_at", "created_by", "description", "icon", "id", "name", "org_id", "repository_url", "slug", "status", "updated_at"]},
  "ProjectList": {"kind": "object", "fields": {"projects": {"ts": "projects", "codec": () => CODECS["ProjectList.projects"]}}, "required": ["projects"]},
  "ProjectList.projects": {"kind": "array", "items": () => CODECS["Project"]},
  "ProviderStatus": {"kind": "object", "fields": {"capabilities": {"ts": "capabilities", "codec": () => CODECS["ProviderStatus.capabilities"]}, "name": {"ts": "name"}, "region": {"ts": "region"}, "version": {"ts": "version"}}, "required": ["capabilities", "name", "region", "version"]},
  "ProviderStatus.capabilities": {"kind": "array"},
  "PublicConfig": {"kind": "object", "fields": {"app_id": {"ts": "appId"}, "auth_base_path": {"ts": "authBasePath"}, "publishable_key": {"ts": "publishableKey"}}, "required": ["app_id", "auth_base_path", "publishable_key"]},
  "Resolution": {"kind": "object", "fields": {"environment": {"ts": "environment"}, "project_id": {"ts": "projectId"}, "service": {"ts": "service"}, "variables": {"ts": "variables", "codec": () => CODECS["Resolution.variables"]}}, "required": ["environment", "project_id", "variables"]},
  "Resolution.variables": {"kind": "array", "items": () => CODECS["ResolvedVariable"]},
  "ResolvedVariable": {"kind": "object", "fields": {"key": {"ts": "key"}, "overrides": {"ts": "overrides", "codec": () => CODECS["ResolvedVariable.overrides"]}, "variable": {"ts": "variable", "codec": () => CODECS["Variable"]}}, "required": ["key", "overrides", "variable"]},
  "ResolvedVariable.overrides": {"kind": "array", "items": () => CODECS["Variable"]},
  "Resource": {"kind": "object", "fields": {"confidence": {"ts": "confidence"}, "evidence": {"ts": "evidence", "codec": () => CODECS["Resource.evidence"]}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "needs_confirmation": {"ts": "needsConfirmation"}}, "required": ["confidence", "evidence", "id", "kind", "needs_confirmation"]},
  "Resource.evidence": {"kind": "array", "items": () => CODECS["Evidence"]},
  "Service": {"kind": "object", "fields": {"commands": {"ts": "commands", "codec": () => CODECS["Commands"]}, "confidence": {"ts": "confidence"}, "deployable": {"ts": "deployable"}, "ecosystem": {"ts": "ecosystem"}, "entrypoint": {"ts": "entrypoint"}, "evidence": {"ts": "evidence", "codec": () => CODECS["Service.evidence"]}, "framework": {"ts": "framework"}, "id": {"ts": "id"}, "image": {"ts": "image"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "path": {"ts": "path"}, "ports": {"ts": "ports", "codec": () => CODECS["Service.ports"]}, "runtime": {"ts": "runtime"}, "unit": {"ts": "unit"}}, "required": ["commands", "confidence", "deployable", "ecosystem", "entrypoint", "evidence", "id", "kind", "name", "path"]},
  "Service.evidence": {"kind": "array", "items": () => CODECS["Evidence"]},
  "Service.ports": {"kind": "array"},
  "Session": {"kind": "object", "fields": {"org_id": {"ts": "orgId"}, "permissions": {"ts": "permissions", "codec": () => CODECS["Session.permissions"]}, "user_id": {"ts": "userId"}}, "required": ["org_id", "permissions", "user_id"]},
  "Session.permissions": {"kind": "array"},
  "StartAnalysisInputBody": {"kind": "object", "fields": {"ref": {"ts": "ref"}}},
  "Summary": {"kind": "object", "fields": {"deployable": {"ts": "deployable"}, "needs_confirmation": {"ts": "needsConfirmation"}, "packages": {"ts": "packages"}, "resources": {"ts": "resources"}, "services": {"ts": "services"}}, "required": ["deployable", "needs_confirmation", "packages", "resources", "services"]},
  "UpdateProjectInputBody": {"kind": "object", "fields": {"color": {"ts": "color"}, "description": {"ts": "description"}, "icon": {"ts": "icon"}, "name": {"ts": "name"}, "repository_url": {"ts": "repositoryUrl"}}},
  "UpdateVariableInputBody": {"kind": "object", "fields": {"secret": {"ts": "secret"}, "value": {"ts": "value"}}, "required": ["value"]},
  "Variable": {"kind": "object", "fields": {"created_at": {"ts": "createdAt"}, "created_by": {"ts": "createdBy"}, "environment": {"ts": "environment"}, "id": {"ts": "id"}, "key": {"ts": "key"}, "org_id": {"ts": "orgId"}, "project_id": {"ts": "projectId"}, "scope": {"ts": "scope"}, "secret": {"ts": "secret"}, "service": {"ts": "service"}, "updated_at": {"ts": "updatedAt"}, "updated_by": {"ts": "updatedBy"}, "value": {"ts": "value"}}, "required": ["created_at", "created_by", "environment", "id", "key", "org_id", "scope", "secret", "updated_at", "updated_by", "value"]},
  "VariableList": {"kind": "object", "fields": {"variables": {"ts": "variables", "codec": () => CODECS["VariableList.variables"]}}, "required": ["variables"]},
  "VariableList.variables": {"kind": "array", "items": () => CODECS["Variable"]},
  "Workspace": {"kind": "object", "fields": {"ecosystem": {"ts": "ecosystem"}, "evidence": {"ts": "evidence", "codec": () => CODECS["Workspace.evidence"]}, "members": {"ts": "members", "codec": () => CODECS["Workspace.members"]}, "path": {"ts": "path"}, "tool": {"ts": "tool"}}, "required": ["ecosystem", "evidence", "members", "path", "tool"]},
  "Workspace.evidence": {"kind": "array", "items": () => CODECS["Evidence"]},
  "Workspace.members": {"kind": "array"},
};

/** Convert a wire payload into its TypeScript shape. */
export function decode<T>(value: unknown, codecRef?: CodecRef): T {
  return decodeRef<T>(value, typeof codecRef === 'string' ? CODECS[codecRef] : codecRef);
}

/** Convert a TypeScript value into its wire shape. */
export function encode(value: unknown, codecRef?: CodecRef): unknown {
  return encodeRef(value, typeof codecRef === 'string' ? CODECS[codecRef] : codecRef);
}
