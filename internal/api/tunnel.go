package api

import (
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
	"github.com/ShirazuNagisa/isc-core/internal/tunnel"
)

// 本文件实现 Cloudflare 隧道的接口。
//
// 三个端点而不是一个：状态是只读的、开关是写操作，而权限矩阵是按方法
// 区分的（见 internal/api/auth.go）。把它们合成一个"设置隧道"的端点会
// 让只读角色拿到一个能改状态的入口。

// GetTunnelStatus 实现 GET /v1/tunnel。
func (s *Server) GetTunnelStatus(w http.ResponseWriter, r *http.Request) {
	if s.Tunnel == nil {
		// 内核的这一份没有隧道能力（库的使用者可以只要别的部分）。
		// 返回"未开启"而不是 404：界面据此画一个灰掉的开关，
		// 而不是弹一个"接口不存在"—— 后者看起来像内核坏了。
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.TunnelStatus{
			Enabled: false,
			State:   gen.TunnelStatusState(tunnel.StateDisabled),
			Name:    tunnel.DefaultName,
		})
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenTunnelStatus(s.Tunnel.Status()))
}

// EnableTunnel 实现 POST /v1/tunnel/enable。
//
// # 先把开关落库，再尝试启动
//
// 顺序不能反。反过来的话，"缺 cloudflared"或"缺账号授权"这类失败会让
// 设置回到关闭 —— 而用户明明表达了"我要用它"，只是这一步还没做完。
// 界面于是显示成"我点了但开关自己弹回去了"，而真实原因（缺东西）被
// 这个假象盖住。
func (s *Server) EnableTunnel(w http.ResponseWriter, r *http.Request) {
	if s.Tunnel == nil {
		writeProblem(w, r, s.Log, http.StatusNotImplemented,
			CodeInvalidRequest, "error.invalid_request",
			"this kernel build has no tunnel support")
		return
	}
	if err := s.setTunnelEnabled(r, true); err != nil {
		s.internalError(w, r, "api.tunnel.save_failed", err)
		return
	}
	// 启动失败**不回滚开关**：状态里会如实报出缺什么，界面照着显示。
	if err := s.Tunnel.Start(r.Context()); err != nil {
		s.Log.Warn("tunnel did not start", "err", err)
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenTunnelStatus(s.Tunnel.Status()))
}

// DisableTunnel 实现 POST /v1/tunnel/disable。
func (s *Server) DisableTunnel(w http.ResponseWriter, r *http.Request) {
	if s.Tunnel == nil {
		writeProblem(w, r, s.Log, http.StatusNotImplemented,
			CodeInvalidRequest, "error.invalid_request",
			"this kernel build has no tunnel support")
		return
	}
	s.Tunnel.Stop()
	if err := s.setTunnelEnabled(r, false); err != nil {
		s.internalError(w, r, "api.tunnel.save_failed", err)
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenTunnelStatus(s.Tunnel.Status()))
}

// setTunnelEnabled 把开关落库。
func (s *Server) setTunnelEnabled(r *http.Request, enabled bool) error {
	if s.Settings == nil {
		return nil
	}
	_, err := s.Settings.Update(r.Context(), settings.Patch{TunnelEnabled: &enabled})
	return err
}

// toGenTunnelStatus 转换一次隧道状态。
func toGenTunnelStatus(st tunnel.Status) gen.TunnelStatus {
	out := gen.TunnelStatus{
		Enabled:     st.Enabled,
		State:       gen.TunnelStatusState(st.State),
		Name:        st.Name,
		Connections: st.Connections,
		ProxyPort:   st.ProxyPort,
	}
	if st.ID != "" {
		out.Id = &st.ID
	}
	if st.Hostname != "" {
		out.Hostname = &st.Hostname
	}
	if st.Binary != "" {
		out.Binary = &st.Binary
	}
	if st.LastError != "" {
		out.LastError = &st.LastError
	}
	if len(st.LogTail) > 0 {
		out.LogTail = &st.LogTail
	}
	return out
}
