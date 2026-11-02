import type { Codec } from '../codec-runtime';
import { codec_OpenCompatModel } from './OpenCompatModel';

export const codec_ProviderKey_models: Codec = {"kind": "array", "items": () => codec_OpenCompatModel};
