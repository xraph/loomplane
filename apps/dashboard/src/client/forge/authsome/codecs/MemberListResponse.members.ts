import type { Codec } from '../codec-runtime';
import { codec_Member } from './Member';

export const codec_MemberListResponse_members: Codec = {"kind": "array", "items": () => codec_Member};
