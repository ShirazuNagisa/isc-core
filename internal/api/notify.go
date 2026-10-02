package api

import (
	"encoding/json"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/notify"
)

// 本文件实现通知通道的配置与投递记录。
//
// 投递记录那个端点是这里最有用的东西：它回答配置通道时最常被问到的
// 问题 —— **"我的通知到底发出去了没有"**。只写日志的话，用户得去翻
// 日志文件才答得上来。

// ListNotifyChannels 实现 GET /v1/notify/channels。
func (s *Server) ListNotifyChannels(w http.ResponseWriter, _ *http.Request) {
	if s.NotifyConfig == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json",
			gen.NotifyChannelList{Items: []gen.NotifyChannel{}})
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json",
		toGenNotifyChannels(s.NotifyConfig.Configs()))
}

// ReplaceNotifyChannels 实现 PUT /v1/notify/channels。
func (s *Server) ReplaceNotifyChannels(w http.ResponseWriter, r *http.Request) {
	if s.NotifyConfig == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", i18n.T("api.notify.no_center"))
		return
	}

	var in gen.NotifyChannelList
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.T("api.empty_body"))
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	configs := make([]notify.ChannelConfig, 0, len(in.Items))
	for _, item := range in.Items {
		cfg := notify.ChannelConfig{
			ID:   item.Id,
			Name: item.Name,
			Kind: string(item.Kind),
		}
		if item.Enabled != nil {
			cfg.Enabled = *item.Enabled
		} else {
			// 默认启用：用户新建一条通道时的意图几乎总是"让它生效"。
			cfg.Enabled = true
		}
		if item.Url != nil {
			cfg.URL = *item.Url
		}
		if item.Method != nil {
			cfg.Method = *item.Method
		}
		if item.Headers != nil {
			cfg.Headers = *item.Headers
		}
		if item.BodyTemplate != nil {
			cfg.BodyTemplate = *item.BodyTemplate
		}
		if item.MinSeverity != nil {
			cfg.MinSeverity = notify.Severity(*item.MinSeverity)
		}
		configs = append(configs, cfg)
	}

	// Save 会先校验、再逐个构造一遍（把模板语法错误提前暴露）、
	// 最后才落库。失败时原因原样返回 —— 那是用户唯一能据此行动的线索。
	if err := s.NotifyConfig.Save(r.Context(), configs); err != nil {
		s.auditFailure(r, audit.ActionNotifyChannels, "channels", err)
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "notify.invalid_channels", err.Error())
		return
	}

	s.auditSuccess(r, audit.ActionNotifyChannels, "channels", "")
	writeJSON(w, s.Log, http.StatusOK, "application/json",
		toGenNotifyChannels(s.NotifyConfig.Configs()))
}

// ListNotifyDeliveries 实现 GET /v1/notify/deliveries。
func (s *Server) ListNotifyDeliveries(w http.ResponseWriter, _ *http.Request) {
	if s.Notify == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json",
			gen.NotifyDeliveryList{Items: []gen.NotifyDelivery{}})
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json",
		toGenDeliveries(s.Notify.Deliveries()))
}

// TestNotifyChannels 实现 POST /v1/notify/test。
func (s *Server) TestNotifyChannels(w http.ResponseWriter, r *http.Request) {
	if s.Notify == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", i18n.T("api.notify.no_center"))
		return
	}

	// SendNow 是同步的，且绕过去重与队列 —— 用户点了按钮之后期待
	// 立刻看到结果，而不是等下一个投递循环。
	results := s.Notify.SendNow(r.Context(), notify.Message{
		Event:    "notify.test",
		Severity: notify.SeverityInfo,
		Title:    i18n.T("api.notify.test_title"),
		Body:     i18n.T("api.notify.test_body"),
		At:       time.Now().UTC(),
	})

	writeJSON(w, s.Log, http.StatusOK, "application/json",
		toGenDeliveries(results))
}

func toGenNotifyChannels(configs []notify.ChannelConfig) gen.NotifyChannelList {
	resp := gen.NotifyChannelList{Items: make([]gen.NotifyChannel, 0, len(configs))}
	for _, cfg := range configs {
		item := gen.NotifyChannel{
			Id:      cfg.ID,
			Name:    cfg.Name,
			Kind:    gen.NotifyChannelKind(cfg.Kind),
			Enabled: &cfg.Enabled,
		}
		if cfg.URL != "" {
			item.Url = &cfg.URL
		}
		if cfg.Method != "" {
			item.Method = &cfg.Method
		}
		if len(cfg.Headers) > 0 {
			item.Headers = &cfg.Headers
		}
		if cfg.BodyTemplate != "" {
			item.BodyTemplate = &cfg.BodyTemplate
		}
		if cfg.MinSeverity != "" {
			sev := gen.NotifyChannelMinSeverity(cfg.MinSeverity)
			item.MinSeverity = &sev
		}
		resp.Items = append(resp.Items, item)
	}
	return resp
}

func toGenDeliveries(list []notify.Delivery) gen.NotifyDeliveryList {
	resp := gen.NotifyDeliveryList{Items: make([]gen.NotifyDelivery, 0, len(list))}
	for _, d := range list {
		item := gen.NotifyDelivery{
			Channel: d.Channel,
			Kind:    d.Kind,
			Ok:      d.OK,
			At:      d.At,
		}
		if d.Error != "" {
			item.Error = &d.Error
		}
		resp.Items = append(resp.Items, item)
	}
	return resp
}
