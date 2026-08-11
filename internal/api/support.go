package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"gamenode/internal/audit"
	"gamenode/internal/support"
)

func (s *Server) supportBundleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	actor, _, ok := s.requireGlobalPermission(w, r, "Settings.Manage", true)
	if !ok {
		return
	}
	var buffer bytes.Buffer
	if err := s.support.Generate(r.Context(), &buffer, support.Scope{}); err != nil {
		code, summary := "support_bundle_failed", "support bundle generation failed"
		if errors.Is(err, support.ErrBundleTooLarge) {
			code, summary = "support_bundle_too_large", "support bundle exceeds the size limit"
		}
		s.recordAudit(r, auditInput{action: audit.SupportBundleGenerate, resourceType: audit.System, result: audit.Failure, actor: &actor, errorCode: code, errorSummary: summary})
		internal(w)
		return
	}
	metadata, _ := json.Marshal(map[string]any{"bundle_schema_version": 1, "size_bytes": buffer.Len()})
	s.recordAudit(r, auditInput{action: audit.SupportBundleGenerate, resourceType: audit.System, result: audit.Success, actor: &actor, metadata: metadata})
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="gamenode-support-`+time.Now().UTC().Format("20060102T150405Z")+`.zip"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buffer.Bytes())
}
