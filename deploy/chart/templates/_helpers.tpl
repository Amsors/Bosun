{{- define "bosun.labels" -}}
app.kubernetes.io/managed-by: bosun
app.kubernetes.io/part-of: bosun
{{- end }}

{{- define "bosun.image" -}}
{{- printf "%s/%s:%s" .root.Values.global.registry .name .root.Values.global.imageTag -}}
{{- end }}
