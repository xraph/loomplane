import type { Codec } from '../codec-runtime';
import { codec_AppliedInputs } from './AppliedInputs';
import { codec_BuildTarget } from './BuildTarget';
import { codec_CloudPlacement } from './CloudPlacement';
import { codec_CloudSQLApplicationNetwork } from './CloudSQLApplicationNetwork';
import { codec_ComponentConfig } from './ComponentConfig';
import { codec_DatabaseGatewayConfig } from './DatabaseGatewayConfig';
import { codec_ManifestEndpoint } from './ManifestEndpoint';
import { codec_Recipe } from './Recipe';
import { codec_Scale } from './Scale';
import { codec_ServiceConfig_env } from './ServiceConfig.env';
import { codec_ServiceConfig_gateway } from './ServiceConfig.gateway';
import { codec_ServiceConfig_target_settings } from './ServiceConfig.target_settings';
import { codec_Size } from './Size';

export const codec_ServiceConfig: Codec = {"kind": "object", "fields": {"applied_inputs": {"ts": "appliedInputs", "codec": () => codec_AppliedInputs}, "build": {"ts": "build"}, "build_target": {"ts": "buildTarget", "codec": () => codec_BuildTarget}, "catalog": {"ts": "catalog", "codec": () => codec_Recipe}, "cloud_placement": {"ts": "cloudPlacement", "codec": () => codec_CloudPlacement}, "cloud_sql_network": {"ts": "cloudSqlNetwork", "codec": () => codec_CloudSQLApplicationNetwork}, "commit_sha": {"ts": "commitSha"}, "component": {"ts": "component", "codec": () => codec_ComponentConfig}, "context": {"ts": "context"}, "database_gateway": {"ts": "databaseGateway", "codec": () => codec_DatabaseGatewayConfig}, "ecosystem": {"ts": "ecosystem"}, "endpoint": {"ts": "endpoint", "codec": () => codec_ManifestEndpoint}, "env": {"ts": "env", "codec": () => codec_ServiceConfig_env}, "execution": {"ts": "execution"}, "framework": {"ts": "framework"}, "gateway": {"ts": "gateway", "codec": () => codec_ServiceConfig_gateway}, "health_path": {"ts": "healthPath"}, "image": {"ts": "image"}, "internal": {"ts": "internal"}, "port": {"ts": "port"}, "private_egress": {"ts": "privateEgress"}, "provider": {"ts": "provider"}, "public": {"ts": "public"}, "region": {"ts": "region"}, "repo": {"ts": "repo"}, "scale": {"ts": "scale", "codec": () => codec_Scale}, "service": {"ts": "service"}, "size": {"ts": "size", "codec": () => codec_Size}, "source": {"ts": "source"}, "start": {"ts": "start"}, "target": {"ts": "target"}, "target_settings": {"ts": "targetSettings", "codec": () => codec_ServiceConfig_target_settings}, "workload": {"ts": "workload"}}, "required": ["ecosystem", "execution", "provider", "public", "service", "source", "workload"]};
