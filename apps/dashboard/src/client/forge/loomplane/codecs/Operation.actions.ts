import type { Codec } from '../codec-runtime';
import { codec_InfrastructureAction } from './InfrastructureAction';

export const codec_Operation_actions: Codec = {"kind": "array", "items": () => codec_InfrastructureAction};
