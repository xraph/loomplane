import type { Codec } from '../codec-runtime';
import { codec_Scale } from './Scale';
import { codec_ServiceConfig_env } from './ServiceConfig.env';
import { codec_Size } from './Size';

export const codec_ServiceConfig: Codec = {"kind": "object", "fields": {"build": {"ts": "build"}, "commit_sha": {"ts": "commitSha"}, "ecosystem": {"ts": "ecosystem"}, "env": {"ts": "env", "codec": () => codec_ServiceConfig_env}, "execution": {"ts": "execution"}, "framework": {"ts": "framework"}, "image": {"ts": "image"}, "port": {"ts": "port"}, "provider": {"ts": "provider"}, "public": {"ts": "public"}, "region": {"ts": "region"}, "repo": {"ts": "repo"}, "scale": {"ts": "scale", "codec": () => codec_Scale}, "service": {"ts": "service"}, "size": {"ts": "size", "codec": () => codec_Size}, "source": {"ts": "source"}, "start": {"ts": "start"}, "workload": {"ts": "workload"}}, "required": ["ecosystem", "execution", "provider", "public", "service", "source", "workload"]};
