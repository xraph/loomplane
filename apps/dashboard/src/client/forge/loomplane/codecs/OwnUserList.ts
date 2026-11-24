import type { Codec } from '../codec-runtime';
import { codec_OwnUserList_users } from './OwnUserList.users';

export const codec_OwnUserList: Codec = {"kind": "object", "fields": {"users": {"ts": "users", "codec": () => codec_OwnUserList_users}}, "required": ["users"]};
