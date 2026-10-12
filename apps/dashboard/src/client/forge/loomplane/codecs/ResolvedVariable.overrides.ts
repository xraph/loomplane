import type { Codec } from '../codec-runtime';
import { codec_Variable } from './Variable';

export const codec_ResolvedVariable_overrides: Codec = {"kind": "array", "items": () => codec_Variable};
