import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_resource } from './EditInputBody.add_resource';
import { codec_EditInputBody_placement } from './EditInputBody.placement';

export const codec_EditInputBody: Codec = {"kind": "object", "fields": {"add_resource": {"ts": "addResource", "codec": () => codec_EditInputBody_add_resource}, "ignore": {"ts": "ignore"}, "placement": {"ts": "placement", "codec": () => codec_EditInputBody_placement}}};
