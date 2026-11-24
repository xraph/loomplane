import type { Codec } from '../codec-runtime';
import { codec_OwnUser } from './OwnUser';

export const codec_OwnUserList_users: Codec = {"kind": "array", "items": () => codec_OwnUser};
