import type { Codec } from '../codec-runtime';
import { codec_OrgMember } from './OrgMember';

export const codec_OrgMemberList_members: Codec = {"kind": "array", "items": () => codec_OrgMember};
