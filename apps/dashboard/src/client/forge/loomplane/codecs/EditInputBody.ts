import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_resource } from './EditInputBody.add_resource';

export const codec_EditInputBody: Codec = {"kind": "object", "fields": {"add_resource": {"ts": "addResource", "codec": () => codec_EditInputBody_add_resource}, "ignore": {"ts": "ignore"}}};
