import type { Codec } from '../codec-runtime';
import { codec_UnreachableAccount } from './UnreachableAccount';

export const codec_GPUTargetList_unreachable: Codec = {"kind": "array", "items": () => codec_UnreachableAccount};
