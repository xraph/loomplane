import type { Codec } from '../codec-runtime';
import { codec_BindingList_bindings } from './BindingList.bindings';

export const codec_BindingList: Codec = {"kind": "object", "fields": {"bindings": {"ts": "bindings", "codec": () => codec_BindingList_bindings}}, "required": ["bindings"]};
