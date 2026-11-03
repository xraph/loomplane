import type { Codec } from '../codec-runtime';

export const codec_EffectiveView: Codec = {"kind": "object", "fields": {"base_url": {"ts": "baseUrl"}, "model": {"ts": "model"}, "provider": {"ts": "provider"}, "summary_model": {"ts": "summaryModel"}}, "required": ["model", "provider", "summary_model"]};
