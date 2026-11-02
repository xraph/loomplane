import type { Codec } from '../codec-runtime';
import { codec_GPUTargetList_targets } from './GPUTargetList.targets';
import { codec_GPUTargetList_unreachable } from './GPUTargetList.unreachable';

export const codec_GPUTargetList: Codec = {"kind": "object", "fields": {"targets": {"ts": "targets", "codec": () => codec_GPUTargetList_targets}, "unreachable": {"ts": "unreachable", "codec": () => codec_GPUTargetList_unreachable}}, "required": ["targets", "unreachable"]};
