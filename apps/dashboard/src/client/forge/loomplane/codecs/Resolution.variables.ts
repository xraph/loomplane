import type { Codec } from '../codec-runtime';
import { codec_ResolvedVariable } from './ResolvedVariable';

export const codec_Resolution_variables: Codec = {"kind": "array", "items": () => codec_ResolvedVariable};
