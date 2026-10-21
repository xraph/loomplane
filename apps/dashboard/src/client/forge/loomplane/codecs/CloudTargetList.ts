import type { Codec } from '../codec-runtime';
import { codec_CloudTargetList_targets } from './CloudTargetList.targets';

export const codec_CloudTargetList: Codec = {"kind": "object", "fields": {"targets": {"ts": "targets", "codec": () => codec_CloudTargetList_targets}}, "required": ["targets"]};
