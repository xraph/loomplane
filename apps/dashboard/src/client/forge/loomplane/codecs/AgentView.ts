import type { Codec } from '../codec-runtime';
import { codec_BudgetState } from './BudgetState';
import { codec_EffectiveView } from './EffectiveView';
import { codec_KeyView } from './KeyView';
import { codec_KnowledgeStatus } from './KnowledgeStatus';
import { codec_ServerDefaults } from './ServerDefaults';

export const codec_AgentView: Codec = {"kind": "object", "fields": {"available": {"ts": "available"}, "budget": {"ts": "budget", "codec": () => codec_BudgetState}, "effective": {"ts": "effective", "codec": () => codec_EffectiveView}, "enabled": {"ts": "enabled"}, "key": {"ts": "key", "codec": () => codec_KeyView}, "knowledge": {"ts": "knowledge"}, "knowledge_status": {"ts": "knowledgeStatus", "codec": () => codec_KnowledgeStatus}, "model": {"ts": "model"}, "nexus_tenant_id": {"ts": "nexusTenantId"}, "org_id": {"ts": "orgId"}, "problem": {"ts": "problem"}, "provider_key": {"ts": "providerKey"}, "revision": {"ts": "revision"}, "server": {"ts": "server", "codec": () => codec_ServerDefaults}, "updated_at": {"ts": "updatedAt"}, "updated_by": {"ts": "updatedBy"}}, "required": ["available", "budget", "effective", "enabled", "knowledge", "knowledge_status", "model", "nexus_tenant_id", "org_id", "provider_key", "revision", "server", "updated_at"]};
