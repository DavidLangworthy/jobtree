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

{{- define "gpu-fleet.schedulerName" -}}
{{- printf "%s-scheduler" (include "gpu-fleet.name" .) -}}
{{- end -}}

{{/*
Resolve one component's image reference. Called as
`include "gpu-fleet.image" (list $ .Values.controller.image)`.

The tag falls back component -> image.tag -> .Chart.AppVersion. The last hop is
what makes a released chart self-consistent: release.yaml packages the chart
with `--app-version <tag>` after pushing images under exactly that tag, so an
install with no flags at all pulls images that exist. `required` refuses to
render a component whose repository was blanked, rather than emitting a
`:tag`-only reference the kubelet would reject at pull time.
*/}}
{{- define "gpu-fleet.image" -}}
{{- $root := index . 0 -}}
{{- $img := index . 1 -}}
{{- $repo := required "a component image.repository must be set" $img.repository -}}
{{- $tag := $img.tag | default $root.Values.image.tag | default $root.Chart.AppVersion -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}

{{- define "gpu-fleet.imagePullPolicy" -}}
{{- $root := index . 0 -}}
{{- $img := index . 1 -}}
{{- $img.pullPolicy | default $root.Values.image.pullPolicy -}}
{{- end -}}
