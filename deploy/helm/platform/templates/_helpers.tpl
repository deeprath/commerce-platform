{{/* Common labels for generated Applications. */}}
{{- define "platform.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: commerce-platform
{{- end -}}
