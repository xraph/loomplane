import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_resource } from './EditInputBody.add_resource';
import { codec_EditInputBody_link } from './EditInputBody.link';
import { codec_EditInputBody_placement } from './EditInputBody.placement';
import { codec_EditInputBody_set_use } from './EditInputBody.set_use';
import { codec_EditInputBody_unlink } from './EditInputBody.unlink';

export const codec_EditInputBody: Codec = {"kind": "object", "fields": {"add_resource": {"ts": "addResource", "codec": () => codec_EditInputBody_add_resource}, "ignore": {"ts": "ignore"}, "link": {"ts": "link", "codec": () => codec_EditInputBody_link}, "placement": {"ts": "placement", "codec": () => codec_EditInputBody_placement}, "set_use": {"ts": "setUse", "codec": () => codec_EditInputBody_set_use}, "unlink": {"ts": "unlink", "codec": () => codec_EditInputBody_unlink}}};
