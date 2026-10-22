import type { Codec } from '../codec-runtime';
import { codec_ProjectGrant } from './ProjectGrant';

export const codec_CloudProjectGrantList_grants: Codec = {"kind": "array", "items": () => codec_ProjectGrant};
