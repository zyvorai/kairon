{{/*
kairon.ui.usersConfigured returns "true" when either ui.auth.users or
ui.auth.existingSecret names a real username/password login account
source. Shared between all.yaml and NOTES.txt so they can never disagree
about whether real login accounts are configured.
*/}}
{{- define "kairon.ui.usersConfigured" -}}
{{- if or (gt (len .Values.ui.auth.users) 0) .Values.ui.auth.existingSecret -}}true{{- end -}}
{{- end -}}

{{/*
kairon.ui.anyAuthConfigured returns "true" when kairon-ui has any explicit
auth configured: a shared token, a pre-created token Secret, real
username/password accounts, or an explicit unauthenticated opt-in. When
this is false, the chart seeds a default "admin" account instead of
refusing to install (see all.yaml / NOTES.txt).
*/}}
{{- define "kairon.ui.anyAuthConfigured" -}}
{{- if or .Values.ui.allowUnauthenticated .Values.ui.token .Values.ui.existingSecret (eq (include "kairon.ui.usersConfigured" .) "true") -}}true{{- end -}}
{{- end -}}
