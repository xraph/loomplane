import type { Codec } from '../codec-runtime';
import { codec_ResolvedVariable_overrides } from './ResolvedVariable.overrides';
import { codec_Variable } from './Variable';

export const codec_ResolvedVariable: Codec = {"kind": "object", "fields": {"key": {"ts": "key"}, "overrides": {"ts": "overrides", "codec": () => codec_ResolvedVariable_overrides}, "variable": {"ts": "variable", "codec": () => codec_Variable}}, "required": ["key", "overrides", "variable"]};
