import type { Codec } from '../codec-runtime';
import { codec_KindInfo_components } from './KindInfo.components';
import { codec_KindInfo_consumer_options } from './KindInfo.consumer_options';
import { codec_KindInfo_dependencies } from './KindInfo.dependencies';
import { codec_KindInfo_outputs } from './KindInfo.outputs';
import { codec_KindInfo_targets } from './KindInfo.targets';
import { codec_KindInfo_versions } from './KindInfo.versions';

export const codec_KindInfo: Codec = {"kind": "object", "fields": {"components": {"ts": "components", "codec": () => codec_KindInfo_components}, "consumer_options": {"ts": "consumerOptions", "codec": () => codec_KindInfo_consumer_options}, "dependencies": {"ts": "dependencies", "codec": () => codec_KindInfo_dependencies}, "description": {"ts": "description"}, "engine": {"ts": "engine"}, "feature": {"ts": "feature"}, "name": {"ts": "name"}, "outputs": {"ts": "outputs", "codec": () => codec_KindInfo_outputs}, "placement": {"ts": "placement"}, "targets": {"ts": "targets", "codec": () => codec_KindInfo_targets}, "title": {"ts": "title"}, "versions": {"ts": "versions", "codec": () => codec_KindInfo_versions}}, "required": ["engine", "name", "placement", "targets", "title", "versions"]};
