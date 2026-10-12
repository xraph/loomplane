import type { Codec } from '../codec-runtime';
import { codec_RevisionList_revisions } from './RevisionList.revisions';

export const codec_RevisionList: Codec = {"kind": "object", "fields": {"revisions": {"ts": "revisions", "codec": () => codec_RevisionList_revisions}}, "required": ["revisions"]};
