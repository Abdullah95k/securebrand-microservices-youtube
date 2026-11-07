{{/* Names and labels. */}}
{{- define "yts.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "yts.fullname" -}}
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

{{- define "yts.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ include "yts.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: securebrand
{{- end -}}

{{/* Selector labels for one component: pass (dict "ctx" $ "component" "worker"). */}}
{{- define "yts.selector" -}}
app.kubernetes.io/name: {{ include "yts.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "yts.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "yts.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* The image: by digest when given, else by tag (default: appVersion). */}}
{{- define "yts.image" -}}
{{- $repo := required "image.repository is required (the platform's pipeline sets it)" .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" $repo .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" $repo (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{/* Storage settings must be complete when a store is on (unless they come
from the platform's credentials). */}}
{{- define "yts.checkStorage" -}}
{{- range $name, $s := .Values.storage -}}
{{- if and $s.enabled (not $s.fromPlatformCredentials) -}}
{{- $_ := required (printf "storage.%s.endpoint is required when storage.%s.enabled" $name $name) $s.endpoint -}}
{{- $_ := required (printf "storage.%s.region is required when storage.%s.enabled" $name $name) $s.region -}}
{{- $_ := required (printf "storage.%s.bucket is required when storage.%s.enabled" $name $name) $s.bucket -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Whether pods mount the platform's credentials: for the database, for
a store that takes its settings from them, or for the relay's Kafka. Prints
"true" or nothing. */}}
{{- define "yts.platformCredentials" -}}
{{- $s := .Values.storage -}}
{{- if or .Values.database.platformCredentials (and $s.media.enabled $s.media.fromPlatformCredentials) (and $s.raw.enabled $s.raw.fromPlatformCredentials) .Values.relay.enabled -}}
true
{{- end -}}
{{- end -}}

{{/* Environment shared by every yts container: where the secrets are. */}}
{{- define "yts.env" -}}
{{- if .Values.database.platformCredentials }}
- name: YT_POSTGRES_DIR
  value: /etc/platform-credentials
{{- else }}
- name: YT_DATABASE_URL_FILE
  value: /etc/yts-secrets/database_url
{{- end }}
- name: YT_PRIVACY_KEY_FILE
  value: /etc/yts-secrets/privacy_key
- name: YT_API_KEY_FILE
  value: /etc/yts-secrets/youtube_api_key
- name: YT_WEBSUB_SECRET_FILE
  value: /etc/yts-secrets/websub_secret
- name: YT_DEBUG_DIR
  value: /var/lib/securebrand-youtube/debug
- name: YT_BACKUP_DIR
  value: /var/lib/securebrand-youtube/backups
{{- if .Values.storage.media.enabled }}
- name: YT_STORAGE_FILE
  value: /etc/yts-config/storage.json
{{- end }}
{{- if .Values.storage.raw.enabled }}
- name: YT_RAW_STORAGE_FILE
  value: /etc/yts-config/raw_storage.json
{{- end }}
{{- if and .Values.websub.enabled .Values.websub.callback }}
- name: YT_WEBSUB_CALLBACK
  value: {{ .Values.websub.callback | quote }}
{{- end }}
{{- end -}}

{{/* Volumes shared by every yts pod. */}}
{{- define "yts.volumes" -}}
- name: tmp
  emptyDir:
    sizeLimit: {{ .Values.tmpVolume.sizeLimit }}
- name: data
  emptyDir:
    sizeLimit: {{ .Values.dataVolume.sizeLimit }}
- name: yts-secrets
  secret:
    secretName: {{ .Values.secrets.yts }}
    defaultMode: {{ .Values.secrets.defaultMode }}
{{- if include "yts.platformCredentials" . }}
- name: platform-credentials
  secret:
    secretName: {{ .Values.secrets.platformCredentials }}
    defaultMode: {{ .Values.secrets.defaultMode }}
{{- end }}
{{- if or .Values.storage.media.enabled .Values.storage.raw.enabled }}
- name: config
  configMap:
    name: {{ include "yts.fullname" . }}-storage
{{- end }}
{{- end -}}

{{- define "yts.volumeMounts" -}}
- name: tmp
  mountPath: /tmp
- name: data
  mountPath: /var/lib/securebrand-youtube
- name: yts-secrets
  mountPath: /etc/yts-secrets
  readOnly: true
{{- if include "yts.platformCredentials" . }}
- name: platform-credentials
  mountPath: /etc/platform-credentials
  readOnly: true
{{- end }}
{{- if or .Values.storage.media.enabled .Values.storage.raw.enabled }}
- name: config
  mountPath: /etc/yts-config
  readOnly: true
{{- end }}
{{- end -}}

{{/* Pod-level settings shared by every yts pod. */}}
{{- define "yts.podSpec" -}}
serviceAccountName: {{ include "yts.serviceAccountName" . }}
automountServiceAccountToken: false
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
