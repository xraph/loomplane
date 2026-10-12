import type { Codec } from '../codec-runtime';
import { codec_OrgMemberList_members } from './OrgMemberList.members';

export const codec_OrgMemberList: Codec = {"kind": "object", "fields": {"members": {"ts": "members", "codec": () => codec_OrgMemberList_members}}, "required": ["members"]};
