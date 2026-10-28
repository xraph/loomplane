import type { Codec } from '../codec-runtime';
import { codec_Embeddings } from './Embeddings';
import { codec_HuggingFaceToken } from './HuggingFaceToken';
import { codec_View_allocations } from './View.allocations';
import { codec_View_provider_keys } from './View.provider_keys';

export const codec_View: Codec = {"kind": "object", "fields": {"allocated_usd": {"ts": "allocatedUsd"}, "allocations": {"ts": "allocations", "codec": () => codec_View_allocations}, "can_manage": {"ts": "canManage"}, "embeddings": {"ts": "embeddings", "codec": () => codec_Embeddings}, "huggingface": {"ts": "huggingface", "codec": () => codec_HuggingFaceToken}, "monthly_budget_usd": {"ts": "monthlyBudgetUsd"}, "org_id": {"ts": "orgId"}, "provider_keys": {"ts": "providerKeys", "codec": () => codec_View_provider_keys}, "revision": {"ts": "revision"}, "unallocated_usd": {"ts": "unallocatedUsd"}, "updated_at": {"ts": "updatedAt"}, "updated_by": {"ts": "updatedBy"}}, "required": ["allocated_usd", "allocations", "can_manage", "embeddings", "monthly_budget_usd", "org_id", "provider_keys", "revision", "unallocated_usd", "updated_at"]};
