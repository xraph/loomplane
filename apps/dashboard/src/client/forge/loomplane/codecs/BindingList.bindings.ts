import type { Codec } from '../codec-runtime';
import { codec_Binding } from './Binding';

export const codec_BindingList_bindings: Codec = {"kind": "array", "items": () => codec_Binding};
