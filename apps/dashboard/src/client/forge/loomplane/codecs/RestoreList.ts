import type { Codec } from '../codec-runtime';
import { codec_RestoreList_restores } from './RestoreList.restores';

export const codec_RestoreList: Codec = {"kind": "object", "fields": {"restores": {"ts": "restores", "codec": () => codec_RestoreList_restores}}, "required": ["restores"]};
