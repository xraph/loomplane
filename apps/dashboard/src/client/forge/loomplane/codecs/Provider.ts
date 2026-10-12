import type { Codec } from '../codec-runtime';
import { codec_Provider_provisions } from './Provider.provisions';
import { codec_Provider_regions } from './Provider.regions';
import { codec_Provider_runs } from './Provider.runs';

export const codec_Provider: Codec = {"kind": "object", "fields": {"available": {"ts": "available"}, "cloud": {"ts": "cloud"}, "description": {"ts": "description"}, "model": {"ts": "model"}, "name": {"ts": "name"}, "note": {"ts": "note"}, "provisions": {"ts": "provisions", "codec": () => codec_Provider_provisions}, "regions": {"ts": "regions", "codec": () => codec_Provider_regions}, "runs": {"ts": "runs", "codec": () => codec_Provider_runs}, "scaling": {"ts": "scaling"}, "title": {"ts": "title"}}, "required": ["available", "cloud", "description", "model", "name", "scaling", "title"]};
