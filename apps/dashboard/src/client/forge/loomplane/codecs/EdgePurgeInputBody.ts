import type { Codec } from '../codec-runtime';
import { codec_EdgePurgeInputBody_urls } from './EdgePurgeInputBody.urls';

export const codec_EdgePurgeInputBody: Codec = {"kind": "object", "fields": {"revision": {"ts": "revision"}, "urls": {"ts": "urls", "codec": () => codec_EdgePurgeInputBody_urls}}, "required": ["revision", "urls"]};
