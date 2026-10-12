import type { Codec } from '../codec-runtime';
import { codec_Evidence } from './Evidence';

export const codec_BuildSystem: Codec = {"kind": "object", "fields": {"evidence": {"ts": "evidence", "codec": () => codec_Evidence}, "name": {"ts": "name"}, "path": {"ts": "path"}, "source": {"ts": "source"}}, "required": ["evidence", "name", "path"]};
