{{- define "gpu-fleet.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gpu-fleet.labels" -}}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: Helm
app.kubernetes.io/component: scheduler
{{- end -}}

{{- define "gpu-fleet.controllerName" -}}
{{- printf "%s-controller" (include "gpu-fleet.name" .) -}}
{{- end -}}

{{- define "gpu-fleet.notifierName" -}}
{{- printf "%s-notifier" (include "gpu-fleet.name" .) -}}
{{- end -}}
