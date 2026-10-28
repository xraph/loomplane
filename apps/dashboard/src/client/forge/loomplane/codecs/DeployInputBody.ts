import type { Codec } from '../codec-runtime';
import { codec_DeployInputBody_server } from './DeployInputBody.server';
import { codec_DeployInputBody_target } from './DeployInputBody.target';

export const codec_DeployInputBody: Codec = {"kind": "object", "fields": {"entry_id": {"ts": "entryId"}, "max_model_len": {"ts": "maxModelLen"}, "monthly_budget_usd": {"ts": "monthlyBudgetUsd"}, "quantization": {"ts": "quantization"}, "resource": {"ts": "resource"}, "review_server": {"ts": "reviewServer"}, "server": {"ts": "server", "codec": () => codec_DeployInputBody_server}, "target": {"ts": "target", "codec": () => codec_DeployInputBody_target}}, "required": ["entry_id", "target"]};
