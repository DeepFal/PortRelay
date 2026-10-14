package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/DeepFal/PortRelay/core/protocol"
)

// GroupView joins a tunnel group with its members and live shared access state
type GroupView struct {
	TunnelGroup
	TunnelIDs []string              `json:"tunnel_ids"` // 成员，按监听端口排序
	Metrics   protocol.GroupMetrics `json:"metrics"`
	// RateHistory 开启中成员的近 10 分钟流量之和
	RateHistory *RateSeries `json:"rate_history,omitempty"`
	// Renewals 可延长的设备与延长申请（组关闭后 2 小时内仍有）
	Renewals []protocol.RenewalInfo `json:"renewals,omitempty"`
}

func (s *APIServer) groupsView(metrics protocol.MetricsMessage) []GroupView {
	shared := make(map[string]protocol.GroupMetrics, len(metrics.Groups))
	for _, g := range metrics.Groups {
		shared[g.GroupID] = g
	}
	cfg := s.cm.Get()
	out := make([]GroupView, 0, len(cfg.Groups))
	for _, g := range cfg.Groups {
		v := GroupView{TunnelGroup: g, TunnelIDs: make([]string, 0), Metrics: protocol.GroupMetrics{GroupID: g.ID},
			Renewals: renewalsOf(metrics.Renewals, "", g.ID)}
		if m, ok := shared[g.ID]; ok {
			v.Metrics = m
		}
		var open []string
		for _, t := range s.cm.GroupMembers(g.ID) {
			v.TunnelIDs = append(v.TunnelIDs, t.ID)
			if t.Enabled {
				open = append(open, t.ID)
			}
		}
		if len(open) > 0 {
			series := s.agent.GroupRateSeries(open)
			v.RateHistory = &series
		}
		out = append(out, v)
	}
	return out
}

// groupRequest renames a group or moves existing tunnels into it
type groupRequest struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// TunnelIDs 加入该组的已有通道
	TunnelIDs []string `json:"tunnel_ids"`
}

func (req groupRequest) spec() GroupSpec {
	return GroupSpec{Name: req.Name, Owner: req.Owner, MoveIDs: req.TunnelIDs}
}

func decodeGroupRequest(r *http.Request) (groupRequest, error) {
	var req groupRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err == io.EOF {
		err = nil
	}
	return req, err
}

// syncChanged pushes members whose group changed to the relay server
func (s *APIServer) syncChanged(changed []protocol.TunnelConfig) {
	for _, t := range changed {
		if t.Enabled {
			s.agent.SyncTunnel(t, true)
		}
	}
}

// handleGroups: GET 列表；POST 用已有通道组成通道组（新通道在添加通道表单里创建，见 handleTunnelBatch）
func (s *APIServer) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, http.StatusOK, s.groupsView(s.agent.GetLatestMetrics()))
	case http.MethodPost:
		req, err := decodeGroupRequest(r)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		g, changed, err := s.cm.SaveGroup("", req.spec())
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.syncChanged(changed)
		jsonResponse(w, http.StatusCreated, map[string]any{"group": g})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGroupItem:
//
//	PUT    /api/groups/{id}             改名 / 责任人，并可把已有通道加入该组
//	DELETE /api/groups/{id}?delete_members=1   解散（默认保留成员为独立通道）
//	POST   /api/groups/{id}/toggle      {"enabled": bool} 整组开关
//	...    /api/groups/{id}/leases|invites|requests  与单个通道相同的授权接口
func (s *APIServer) handleGroupItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[2] == "" {
		jsonError(w, http.StatusBadRequest, "Missing group ID")
		return
	}
	id := parts[2]
	g, ok := s.cm.FindGroup(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "通道组不存在")
		return
	}

	if len(parts) >= 4 && isAccessPath(parts[3]) {
		s.handleAccess(w, r, Scope{GroupID: id}, parts[3:])
		return
	}
	if len(parts) == 4 && parts[3] == "toggle" && r.Method == http.MethodPost {
		want, err := decodeToggle(r)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if want == nil {
			// 不指定时：有任一成员开启则整组关闭，否则整组开启
			on := false
			for _, t := range s.cm.GroupMembers(id) {
				on = on || t.Enabled
			}
			flip := !on
			want = &flip
		}
		out := make([]protocol.TunnelConfig, 0)
		for _, t := range s.cm.GroupMembers(id) {
			updated, err := s.setEnabled(t.ID, want)
			if err != nil {
				jsonError(w, http.StatusConflict, err.Error())
				return
			}
			out = append(out, updated)
		}
		jsonResponse(w, http.StatusOK, out)
		return
	}
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		req, err := decodeGroupRequest(r)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = g.Name
		}
		updated, changed, err := s.cm.SaveGroup(id, req.spec())
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.syncChanged(changed)
		jsonResponse(w, http.StatusOK, map[string]any{"group": updated})

	case http.MethodDelete:
		deleteMembers := r.URL.Query().Get("delete_members") == "1"
		members, err := s.cm.RemoveGroup(id, deleteMembers)
		if err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		for _, t := range members {
			if deleteMembers {
				s.agent.ConsumeSessionBytes(t.ID)
				s.agent.dropUsage(t.ID)
				s.agent.DeleteTunnel(t.ID)
			} else if t.Enabled {
				s.agent.SyncTunnel(t, true)
			}
		}
		s.agent.DeleteGroup(id)
		jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleTunnelBatch: POST /api/tunnels/batch —— 添加通道表单的提交，可以一次创建单个或多个通道。
// 请求体是通道设置，另加：
//
//	target     可以写多个目标，如 "10.0.1.11-13:22, 10.0.2.5:3306"（见 ExpandTargets）
//	group_id   加入已有通道组；new_group 新建通道组。多个目标必须放进通道组
//	dry_run    只预览将创建的通道（名称、目标、分配的端口），不保存
//
// 返回 {"group": 加入的通道组或 null, "tunnels": [...]}
func (s *APIServer) handleTunnelBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		protocol.TunnelConfig
		NewGroup string `json:"new_group"`
		DryRun   bool   `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	group, created, err := s.cm.CreateTunnels(NewTunnels{Template: req.TunnelConfig, NewGroup: req.NewGroup}, req.DryRun)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := http.StatusOK
	if !req.DryRun {
		status = http.StatusCreated
		for _, t := range created {
			s.agent.SyncTunnel(t, false)
		}
	}
	jsonResponse(w, status, map[string]any{"group": group, "tunnels": created})
}
