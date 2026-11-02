import type { Codec } from '../codec-runtime';
import { codec_TeamGrant } from './TeamGrant';

export const codec_Team_grants: Codec = {"kind": "array", "items": () => codec_TeamGrant};
