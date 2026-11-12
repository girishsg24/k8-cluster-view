{{- define "clusterview.name" -}}clusterview{{- end -}}
{{- define "clusterview.labels" -}}
app.kubernetes.io/name: clusterview
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
