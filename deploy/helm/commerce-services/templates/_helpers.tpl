{{/* Merge defaults with a service's own config: (merged defaults svcConfig) */}}
{{- define "commerce.svc" -}}
{{- $merged := deepCopy .defaults -}}
{{- $merged = mergeOverwrite $merged .svc -}}
{{- $merged | toYaml -}}
{{- end -}}

{{- define "commerce.image" -}}
{{- $g := .Values.global.image -}}
{{- $tag := $g.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $g.registry .name $tag -}}
{{- end -}}

{{- define "commerce.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/part-of: commerce-platform
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
helm.sh/chart: {{ .root.Chart.Name }}-{{ .root.Chart.Version }}
app: {{ .name }}
{{- end -}}

{{- define "commerce.selectorLabels" -}}
app: {{ .name }}
{{- end -}}

{{/* GOMEMLIMIT from the container memory limit, as "<N>MiB" (Go accepts MiB). */}}
{{- define "commerce.gomemlimit" -}}
{{- $lim := . -}}
{{- if hasSuffix "Mi" $lim -}}
{{- printf "%dMiB" (int (trimSuffix "Mi" $lim)) -}}
{{- else if hasSuffix "Gi" $lim -}}
{{- printf "%dMiB" (mul (int (trimSuffix "Gi" $lim)) 1024) -}}
{{- else -}}
{{- $lim -}}
{{- end -}}
{{- end -}}
