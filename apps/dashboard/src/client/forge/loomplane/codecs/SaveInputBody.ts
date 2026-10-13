import type { Codec } from '../codec-runtime';
import { codec_SaveInputBody_manifest } from './SaveInputBody.manifest';

export const codec_SaveInputBody: Codec = {"kind": "object", "fields": {"base_revision": {"ts": "baseRevision"}, "manifest": {"ts": "manifest", "codec": () => codec_SaveInputBody_manifest}, "yaml": {"ts": "yaml"}}};
