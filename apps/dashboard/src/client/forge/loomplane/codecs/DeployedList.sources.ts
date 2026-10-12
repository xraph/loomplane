import type { Codec } from '../codec-runtime';
import { codec_DeployedRepository } from './DeployedRepository';

export const codec_DeployedList_sources: Codec = {"kind": "array", "items": () => codec_DeployedRepository};
