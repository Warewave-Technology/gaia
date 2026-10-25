{{- define "gaia.name" -}}{{ .Chart.Name }}{{- end -}}

{{- define "gaia.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{- else -}}{{ printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "gaia.labels" -}}
app.kubernetes.io/name: {{ include "gaia.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "gaia.selector" -}}
app.kubernetes.io/name: {{ include "gaia.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gaia.serviceAccount" -}}
{{- if .Values.serviceAccount.create -}}{{ default (include "gaia.fullname" .) .Values.serviceAccount.name }}{{- else -}}{{ default "default" .Values.serviceAccount.name }}{{- end -}}
{{- end -}}

{{- define "gaia.image" -}}
{{ .Values.image.repository }}:{{ default .Chart.AppVersion .Values.image.tag }}
{{- end -}}

{{/* The DSN: a Secret's key or a file. */}}
{{- define "gaia.dsn" -}}
{{- if .file }}
- name: GAIA_DSN_FILE
  value: {{ .file | quote }}
{{- else if .existingSecret }}
- name: GAIA_DSN
  valueFrom:
    secretKeyRef: { name: {{ .existingSecret | quote }}, key: {{ .key | quote }} }
{{- else }}
{{- fail "database: set existingSecret or file" }}
{{- end }}
{{- end -}}

{{- define "gaia.env" -}}
{{ include "gaia.dsn" .Values.database }}
- name: GAIA_POD_NAMESPACE
  valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
{{- with .Values.openbao.addr }}
- name: GAIA_BAO_ADDR
  value: {{ . | quote }}
- name: GAIA_BAO_K8S_ROLE
  value: {{ $.Values.openbao.kubernetesRole | quote }}
- name: GAIA_BAO_K8S_MOUNT
  value: {{ $.Values.openbao.mount | quote }}
{{- end }}
{{- end -}}

{{- define "gaia.args" -}}
{{- with .Values.server.repo }}
- --repo
- {{ . | quote }}
{{- end }}
{{- with .Values.server.repoCredentials }}
- --repo-credentials
- {{ . | quote }}
{{- end }}
- --data-dir
- /gaia/data
{{- range .Values.server.args }}
- {{ . | quote }}
{{- end }}
{{- end -}}
