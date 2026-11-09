{{/* Expand the name of the chart. */}}
{{- define "loomplane.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "loomplane.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "loomplane.workerName" -}}
{{- printf "%s-worker" (include "loomplane.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "loomplane.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Labels every object carries. */}}
{{- define "loomplane.labels" -}}
helm.sh/chart: {{ include "loomplane.chart" . }}
app.kubernetes.io/name: {{ include "loomplane.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: loomplane
{{- end -}}

{{/* Selector labels for one component: pass (dict "ctx" $ "component" "api"). */}}
{{- define "loomplane.selectorLabels" -}}
app.kubernetes.io/name: {{ include "loomplane.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "loomplane.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "loomplane.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Image reference: a digest wins over the tag, which defaults to appVersion. */}}
{{- define "loomplane.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion | toString) -}}
{{- end -}}
{{- end -}}

{{/* "main" moves with every commit, so pull it each start. */}}
{{- define "loomplane.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if and (not .Values.image.digest) (eq (.Values.image.tag | default .Chart.AppVersion | toString) "main") -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}

{{- define "loomplane.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- include "loomplane.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "loomplane.storageClaimName" -}}
{{- default (printf "%s-storage" (include "loomplane.fullname" .)) .Values.storage.persistence.existingClaim -}}
{{- end -}}

{{/* publicURL's host, for the ingress and route defaults. */}}
{{- define "loomplane.publicHost" -}}
{{- (urlParse (required "publicURL is required, e.g. https://loomplane.example.com" .Values.publicURL)).host -}}
{{- end -}}

{{/* Annotations that roll the pods when their settings change. */}}
{{- define "loomplane.checksums" -}}
checksum/config: {{ include (print .Template.BasePath "/configmap.yaml") . | sha256sum }}
{{- if not .Values.secrets.existingSecret }}
checksum/secret: {{ include (print .Template.BasePath "/secret.yaml") . | sha256sum }}
{{- end }}
{{- if .Values.configFile }}
checksum/config-file: {{ .Values.configFile | sha256sum }}
{{- end }}
{{- end -}}

{{/* Settings both processes read, from the ConfigMap and the Secret. */}}
{{- define "loomplane.envFrom" -}}
- configMapRef:
    name: {{ include "loomplane.fullname" . }}
- secretRef:
    name: {{ include "loomplane.secretName" . }}
{{- with .Values.extraEnvFrom }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Volumes both processes mount. Pass (dict "ctx" $ "tmpSize" "2Gi").
*/}}
{{- define "loomplane.volumes" -}}
- name: tmp
  emptyDir:
    sizeLimit: {{ .tmpSize }}
{{- if .ctx.Values.storage.persistence.enabled }}
- name: storage
  persistentVolumeClaim:
    claimName: {{ include "loomplane.storageClaimName" .ctx }}
{{- end }}
{{- if .ctx.Values.configFile }}
- name: config-file
  secret:
    secretName: {{ include "loomplane.fullname" .ctx }}-config
{{- end }}
{{- with .ctx.Values.extraVolumes }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "loomplane.volumeMounts" -}}
- name: tmp
  mountPath: /tmp
{{- if .Values.storage.persistence.enabled }}
- name: storage
  mountPath: /var/lib/loomplane
{{- end }}
{{- if .Values.configFile }}
- name: config-file
  mountPath: /etc/loomplane/config.local.yaml
  subPath: config.local.yaml
  readOnly: true
{{- end }}
{{- with .Values.extraVolumeMounts }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/*
Soft spreading across nodes and zones, unless the component sets its own.
Pass (dict "ctx" $ "component" "api" "constraints" .Values.api.topologySpreadConstraints).
*/}}
{{- define "loomplane.topologySpread" -}}
{{- if .constraints }}
{{- toYaml .constraints }}
{{- else }}
- maxSkew: 1
  topologyKey: kubernetes.io/hostname
  whenUnsatisfiable: ScheduleAnyway
  labelSelector:
    matchLabels:
      {{- include "loomplane.selectorLabels" (dict "ctx" .ctx "component" .component) | nindent 6 }}
- maxSkew: 1
  topologyKey: topology.kubernetes.io/zone
  whenUnsatisfiable: ScheduleAnyway
  labelSelector:
    matchLabels:
      {{- include "loomplane.selectorLabels" (dict "ctx" .ctx "component" .component) | nindent 6 }}
{{- end }}
{{- end -}}

{{/*
Refuses settings that can't be highly available: local files with more than
one replica and no shared volume.
*/}}
{{- define "loomplane.validate" -}}
{{- $_ := required "publicURL is required, e.g. https://loomplane.example.com" .Values.publicURL -}}
{{- if not .Values.secrets.existingSecret -}}
{{- $_ = required "secrets.secretKey is required (openssl rand -base64 32), or name a Secret in secrets.existingSecret" .Values.secrets.secretKey -}}
{{- $_ = required "secrets.databaseURL is required, or name a Secret in secrets.existingSecret" .Values.secrets.databaseURL -}}
{{- /* Loomplane refuses anything else at start; refuse it here instead of crash-looping pods. */ -}}
{{- if ne (len (b64dec .Values.secrets.secretKey)) 32 -}}
{{- fail "secrets.secretKey must be 32 random bytes, base64 encoded: openssl rand -base64 32" -}}
{{- end -}}
{{- end -}}
{{- $replicas := max (int .Values.api.replicaCount) (int .Values.worker.replicaCount) -}}
{{- if .Values.api.autoscaling.enabled }}{{- $replicas = max $replicas (int .Values.api.autoscaling.maxReplicas) -}}{{- end -}}
{{- if .Values.worker.autoscaling.enabled }}{{- $replicas = max $replicas (int .Values.worker.autoscaling.maxReplicas) -}}{{- end -}}
{{- $url := .Values.storage.url | default "" -}}
{{- if and (gt (int $replicas) 1) (not .Values.secrets.existingSecret) (not .Values.storage.persistence.enabled) (not (hasPrefix "s3://" $url)) -}}
{{- fail "More than one replica needs storage they all share: set storage.url to s3://..., or enable storage.persistence with a ReadWriteMany claim. Files on each pod's own disk would be lost to the others." -}}
{{- end -}}
{{- if and .Values.storage.persistence.enabled (gt (int $replicas) 1) (not (has "ReadWriteMany" .Values.storage.persistence.accessModes)) -}}
{{- fail "storage.persistence with more than one replica needs the ReadWriteMany access mode." -}}
{{- end -}}
{{- if not (has (toString .Values.waitlist) (list "" "true" "false")) -}}
{{- fail "waitlist must be true, false or empty." -}}
{{- end -}}
{{- if .Values.inCluster.enabled -}}
{{- if not .Values.inCluster.namespaces -}}
{{- fail "inCluster.enabled needs at least one namespace in inCluster.namespaces for Loomplane to deploy into." -}}
{{- end -}}
{{- if has .Release.Namespace .Values.inCluster.namespaces -}}
{{- fail (printf "inCluster.namespaces can't include %s, where Loomplane itself runs: its database password and secret key live there." .Release.Namespace) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* A separate name and image keep the gateway independent of the API. */}}
{{- define "loomplane.pushGatewayName" -}}
{{- printf "%s-push-gateway" (include "loomplane.fullname" . | trunc 50 | trimSuffix "-") -}}
{{- end -}}

{{- define "loomplane.pushGatewayImage" -}}
{{- include "loomplane.image" (dict "Values" (dict "image" .Values.push.gateway.image) "Chart" .Chart) -}}
{{- end -}}

{{- define "loomplane.validatePushGateway" -}}
{{- $g := .Values.push.gateway -}}
{{- if $g.enabled -}}
{{- $_ := required "push.gateway.existingSecret is required" $g.existingSecret -}}
{{- $_ = required "push.gateway.sealingKeyId is required" $g.sealingKeyId -}}
{{- if not (regexMatch "^[A-Za-z0-9_-]{1,32}$" $g.sealingKeyId) -}}
{{- fail "push.gateway.sealingKeyId must be 1 to 32 letters, digits, underscores or hyphens" -}}
{{- end -}}
{{- if and $g.previousSealingKeyId (or (eq $g.previousSealingKeyId $g.sealingKeyId) (not (regexMatch "^[A-Za-z0-9_-]{1,32}$" $g.previousSealingKeyId))) -}}
{{- fail "push.gateway.previousSealingKeyId must be a different valid sealing key ID" -}}
{{- end -}}
{{- if not (or $g.image.tag $g.image.digest) -}}
{{- fail "Set push.gateway.image.tag or push.gateway.image.digest to a published gateway image" -}}
{{- end -}}
{{- if not (or $g.fcm.enabled $g.apns.enabled) -}}
{{- fail "Enable push.gateway.fcm.enabled or push.gateway.apns.enabled" -}}
{{- end -}}
{{- if and $g.apns.enabled (or (not (regexMatch "^[A-Za-z0-9]{10}$" $g.apns.keyId)) (not (regexMatch "^[A-Za-z0-9]{10}$" $g.apns.teamId))) -}}
{{- fail "APNs needs 10-character push.gateway.apns.keyId and push.gateway.apns.teamId" -}}
{{- end -}}
{{- if not (regexMatch "^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?/?$" .Values.push.gatewayUrl) -}}
{{- fail "push.gatewayUrl must be an HTTPS URL with a bare hostname" -}}
{{- end -}}
{{- if eq (lower (urlParse .Values.push.gatewayUrl).host) (lower (include "loomplane.publicHost" .)) -}}
{{- fail "push.gatewayUrl needs a different hostname from publicURL" -}}
{{- end -}}
{{- if and $g.ingress.enabled (not $g.ingress.tls) -}}
{{- fail "push.gateway.ingress.tls needs a certificate for the gateway hostname" -}}
{{- end -}}
{{- if and $g.httpRoute.enabled (not $g.httpRoute.parentRefs) -}}
{{- fail "push.gateway.httpRoute.parentRefs needs an HTTPS Gateway" -}}
{{- end -}}
{{- end -}}
{{- end -}}
