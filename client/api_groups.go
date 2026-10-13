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
}

func (s *APIServer) groupsView(metrics protocol.MetricsMessage) []GroupView {
	shared := make(map[string]protocol.GroupMetrics, len(metrics.Groups))
	for _, g := range metrics.Groups {
		shared[g.GroupID] = g
	}
	cfg := s.cm.Get()
	out := make([]GroupView, 0, len(cfg.Groups))
	for _, g := range cfg.Groups {
		v := GroupView{TunnelGroup: g, TunnelIDs: make([]string, 0), Metrics: protocol.GroupMetrics{GroupID: g.ID}}
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

// groupRequest creates a group or adds members to one
type groupRequest struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	// Hosts / Ports 展开为新建成员，见 ExpandTargets
	Hosts string `json:"hosts"`
	Ports string `json:"ports"`
	// TunnelIDs 加入该组的已有通道
	TunnelIDs []string `json:"tunnel_ids"`
	// 新建成员的通道设置
	Whitelist        []string `json:"whitelist"`
	IdleCloseMinutes *int     `json:"idle_close_minutes"`
	DurationMinutes  int      `json:"duration_minutes"`
	MaxBandwidthMB   float64  `json:"max_bandwidth_mb"`
}

func (req groupRequest) spec() (GroupSpec, error) {
	spec := GroupSpec{
		Name:    req.Name,
		Owner:   req.Owner,
		MoveIDs: req.TunnelIDs,
		Template: protocol.TunnelConfig{
			Whitelist:        req.Whitelist,
			IdleCloseMinutes: req.IdleCloseMinutes,
			DurationMinutes:  req.DurationMinutes,
			MaxBandwidthMB:   req.MaxBandwidthMB,
		},
	}
	if strings.TrimSpace(req.Hosts) != "" {
		targets, err := ExpandTargets(req.Hosts, req.Ports)
		if err != nil {
			return spec, err
		}
		spec.Targets = targets
	}
	return spec, nil
}

func decodeGroupRequest(r *http.Request) (groupRequest, error) {
	var req groupRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if err == io.EOF {
		err = nil
	}
	return req, err
}

// syncGroupChanges pushes newly created members and changed existing ones to the relay server
func (s *APIServer) syncGroupChanges(created, changed []protocol.TunnelConfig) {
	for _, t := range created {
		s.agent.SyncTunnel(t, false)
	}
	for _, t := range changed {
		if t.Enabled {
			s.agent.SyncTunnel(t, true)
		}
	}
}

// handleGroups: GET 列表；POST 新建通道组（批量新建目标 + / 或加入已有通道）
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
		spec, err := req.spec()
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		g, created, changed, err := s.cm.SaveGroup("", spec)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.syncGroupChanges(created, changed)
		jsonResponse(w, http.StatusCreated, map[string]any{"group": g, "created": nonNil(created)})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleGroupItem:
//
//	POST   /api/groups/preview          预览批量展开结果（不保存）
//	PUT    /api/groups/{id}             改名 / 责任人，并可追加成员
//	DELETE /api/groups/{id}?delete_members=1   解散（默认保留成员为独立通道）
//	POST   /api/groups/{id}/toggle      {"enabled": bool} 整组开关
//	...    /api/groups/{id}/leases|invites|requests  与单个通道相同的授权接口
func (s *APIServer) handleGroupItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[2] == "" {
		jsonError(w, http.StatusBadRequest, "Missing group ID")
		return
	}
	if parts[2] == "preview" && len(parts) == 3 && r.Method == http.MethodPost {
		s.handleGroupPreview(w, r)
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
		spec, err := req.spec()
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		updated, created, changed, err := s.cm.SaveGroup(id, spec)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.syncGroupChanges(created, changed)
		jsonResponse(w, http.StatusOK, map[string]any{"group": updated, "created": nonNil(created)})

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
				s.agent.RemoveTunnel(t.ID)
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

// handleGroupPreview expands hosts × ports and assigns ports without saving
func (s *APIServer) handleGroupPreview(w http.ResponseWriter, r *http.Request) {
	req, err := decodeGroupRequest(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	spec, err := req.spec()
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	members, err := s.cm.PreviewGroupMembers(spec)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	type item struct {
		Name       string `json:"name"`
		Target     string `json:"target"`
		ListenPort int    `json:"listen_port"`
	}
	out := make([]item, 0, len(members))
	for _, t := range members {
		out = append(out, item{t.Name, t.Target, t.ListenPort})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"members": out})
}

func nonNil(ts []protocol.TunnelConfig) []protocol.TunnelConfig {
	if ts == nil {
		return []protocol.TunnelConfig{}
	}
	return ts
}
