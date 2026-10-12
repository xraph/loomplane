import type { Codec } from '../codec-runtime';
import { codec_Evidence } from './Evidence';

export const codec_Requirement: Codec = {"kind": "object", "fields": {"evidence": {"ts": "evidence", "codec": () => codec_Evidence}, "git": {"ts": "git"}, "name": {"ts": "name"}, "version": {"ts": "version"}}, "required": ["evidence", "name"]};
