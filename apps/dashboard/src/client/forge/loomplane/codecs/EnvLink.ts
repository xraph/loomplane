import type { Codec } from '../codec-runtime';
import { codec_EnvLink_options } from './EnvLink.options';

export const codec_EnvLink: Codec = {"kind": "object", "fields": {"access": {"ts": "access"}, "address": {"ts": "address"}, "database": {"ts": "database"}, "droplet_postgres": {"ts": "dropletPostgres"}, "field": {"ts": "field"}, "from": {"ts": "from"}, "gateway": {"ts": "gateway"}, "key": {"ts": "key"}, "options": {"ts": "options", "codec": () => codec_EnvLink_options}, "part": {"ts": "part"}, "subject": {"ts": "subject"}, "user": {"ts": "user"}}, "required": ["from", "key", "subject"]};
