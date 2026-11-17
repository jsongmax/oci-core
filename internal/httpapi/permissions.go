package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"ocicore/internal/accountsvc"
	"ocicore/internal/i18n"
	"ocicore/internal/permsvc"
	"ocicore/internal/store"
)

// handleGetPermissions 返回最近一次权限自检的完整报告。从未自检时 report 为 null。
func (s *Server) handleGetPermissions(w http.ResponseWriter, r *http.Request) {
	report, err := s.st.AccountPermissions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

// handleCheckPermissions 对已保存的账号重新做一次权限自检并保存结果。
func (s *Server) handleCheckPermissions(w http.ResponseWriter, r *http.Request) {
	acc, err := s.st.GetAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	report, err := s.checkPermissions(r.Context(), acc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "credentials", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}

// handleCheckDraftPermissions 在保存之前对一份凭据做权限自检。结果不落库。
func (s *Server) handleCheckDraftPermissions(w http.ResponseWriter, r *http.Request) {
	draft, ok := s.decodeDraft(w, r)
	if !ok {
		return
	}
	client, err := accountsvc.DraftClient(draft)
	if err != nil {
		writeError(w, http.StatusBadRequest, "credentials", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": permsvc.Check(r.Context(), client, "")})
}

// checkPermissions 跑一次权限自检并写回账号。
func (s *Server) checkPermissions(ctx context.Context, acc *store.Account) (*permsvc.Report, error) {
	client, err := s.conns.For(ctx, acc)
	if err != nil {
		return nil, err
	}
	report := permsvc.Check(ctx, client, acc.CompartmentOCID)
	summary, _ := json.Marshal(report.Summary)
	full, _ := json.Marshal(report)
	if err := s.st.SetAccountPermissions(ctx, acc.ID, summary, full); err != nil {
		slog.Warn("保存权限自检结果失败", "account", acc.ID, "err", err)
	}
	return report, nil
}

// checkPermissionsAsync 在后台做权限自检，用于添加账号之后：
// 自检要发十几个请求，不该拖慢保存。
func (s *Server) checkPermissionsAsync(acc *store.Account) {
	go func() {
		ctx, cancel := context.WithTimeout(i18n.WithLang(context.Background(), i18n.Default), 60*time.Second)
		defer cancel()
		if _, err := s.checkPermissions(ctx, acc); err != nil {
			slog.Warn("添加账号后的权限自检失败", "account", acc.ID, "err", err)
		}
	}()
}
