import type { Codec } from '../codec-runtime';
import { codec_ProviderKeyInputBody_fields } from './ProviderKeyInputBody.fields';
import { codec_ProviderKeyInputBody_models } from './ProviderKeyInputBody.models';
import { codec_ProviderKeyInputBody_projects } from './ProviderKeyInputBody.projects';

export const codec_ProviderKeyInputBody: Codec = {"kind": "object", "fields": {"fields": {"ts": "fields", "codec": () => codec_ProviderKeyInputBody_fields}, "models": {"ts": "models", "codec": () => codec_ProviderKeyInputBody_models}, "projects": {"ts": "projects", "codec": () => codec_ProviderKeyInputBody_projects}, "type": {"ts": "type"}}, "required": ["type"]};
