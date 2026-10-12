package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

type LLMModelDTO struct {
	ID              string `json:"id"`
	ProfileID       string `json:"profile_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	DisplayName     string `json:"display_name"`
	Enabled         bool   `json:"enabled"`
	Default         bool   `json:"is_default"`
}

type LLMModelSetDTO struct {
	ProfileID      string        `json:"profile_id"`
	DefaultModelID *string       `json:"default_model_id"`
	Models         []LLMModelDTO `json:"models"`
}

// LLMModelSelectionDTO 是配置态路由；默认模式的 model_id 必须为空。
type LLMModelSelectionDTO struct {
	ProfileID string  `json:"profile_id"`
	ModelMode string  `json:"model_mode"`
	ModelID   *string `json:"model_id"`
}

func llmModelSetDTO(set db.LLMModelSet) LLMModelSetDTO {
	out := LLMModelSetDTO{ProfileID: i64s(set.ProfileID), Models: make([]LLMModelDTO, 0, len(set.Models))}
	if set.DefaultModelID != nil {
		id := i64s(*set.DefaultModelID)
		out.DefaultModelID = &id
	}
	for _, m := range set.Models {
		out.Models = append(out.Models, LLMModelDTO{ID: i64s(m.ID), ProfileID: i64s(m.ProfileID),
			UpstreamModelID: m.UpstreamModelID, DisplayName: m.DisplayName, Enabled: m.Enabled, Default: m.Default})
	}
	return out
}

func llmModelSelectionDTO(selection db.LLMModelSelection) LLMModelSelectionDTO {
	out := LLMModelSelectionDTO{ProfileID: i64s(selection.ProfileID), ModelMode: selection.Normalized().ModelMode}
	if selection.ModelID != nil {
		id := i64s(*selection.ModelID)
		out.ModelID = &id
	}
	return out
}

// llmModelRequestID 同时接收字符串与整数 JSON，避免经过 float64 丢失 ID 精度。
type llmModelRequestID int64

func (id *llmModelRequestID) UnmarshalJSON(raw []byte) error {
	value := strings.TrimSpace(string(raw))
	if len(value) > 0 && value[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	}
	if value == "" {
		return errors.New("ID 不能为空")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return errors.New("ID 必须是非负十进制整数")
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("无效 ID: %w", err)
	}
	*id = llmModelRequestID(n)
	return nil
}

type llmModelSelectionRequest struct {
	ProfileID llmModelRequestID  `json:"profile_id"`
	ModelMode string             `json:"model_mode"`
	ModelID   *llmModelRequestID `json:"model_id"`
}

func optionalIDString(id *int64) *string {
	if id == nil {
		return nil
	}
	value := i64s(*id)
	return &value
}
func llmSelectionsDTO(in []db.LLMModelSelection) []LLMModelSelectionDTO {
	out := make([]LLMModelSelectionDTO, 0, len(in))
	for _, s := range in {
		out = append(out, llmModelSelectionDTO(s))
	}
	return out
}
func modelSelectionRequests(in []llmModelSelectionRequest) []db.LLMModelSelection {
	if in == nil {
		return nil
	}
	out := make([]db.LLMModelSelection, 0, len(in))
	for _, s := range in {
		out = append(out, s.selection(int64(s.ProfileID)))
	}
	return out
}

type LLMResolvedMemberDTO struct {
	ProfileID       string `json:"profile_id"`
	ModelMode       string `json:"model_mode"`
	ModelID         string `json:"model_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	DisplayName     string `json:"display_name"`
	MemberKey       string `json:"member_key"`
}

func resolvedMemberDTO(model *db.LLMModel, mode string) *LLMResolvedMemberDTO {
	if model == nil {
		return nil
	}
	if mode == "" {
		mode = db.LLMModelModeChannelDefault
	}
	return &LLMResolvedMemberDTO{ProfileID: i64s(model.ProfileID), ModelID: i64s(model.ID), ModelMode: mode, UpstreamModelID: model.UpstreamModelID, DisplayName: model.DisplayName, MemberKey: model.MemberKey()}
}

// 三态渠道字段在对话和Agent配置中共用：新字段优先，省略不改，null清除。
type llmBindingRequest struct {
	ProfileID       json.RawMessage `json:"profile_id"`
	LegacyProfileID json.RawMessage `json:"llm_profile_id"`
	ModelMode       *string         `json:"model_mode"`
	ModelID         json.RawMessage `json:"model_id"`
}

func (r llmBindingRequest) selection(currentProfile *int64, currentMode string, currentModel *int64) (*db.LLMModelSelection, bool, error) {
	raw := r.ProfileID
	if raw == nil {
		raw = r.LegacyProfileID
	}
	changed := raw != nil || r.ModelMode != nil || r.ModelID != nil
	if !changed {
		return nil, false, nil
	}
	profile := currentProfile
	mode, model := currentMode, currentModel
	if raw != nil {
		if strings.TrimSpace(string(raw)) == "null" {
			return nil, true, nil
		}
		var value llmModelRequestID
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, true, err
		}
		id := int64(value)
		profile = &id
		mode = db.LLMModelModeChannelDefault
		model = nil
	}
	if profile == nil {
		return nil, true, db.ErrLLMModelInvalid
	}
	if r.ModelMode != nil {
		mode = *r.ModelMode
		if mode == db.LLMModelModeChannelDefault {
			model = nil
		}
	}
	if r.ModelID != nil {
		if strings.TrimSpace(string(r.ModelID)) == "null" {
			model = nil
		} else {
			var value llmModelRequestID
			if err := json.Unmarshal(r.ModelID, &value); err != nil {
				return nil, true, err
			}
			id := int64(value)
			model = &id
		}
	}
	s := db.LLMModelSelection{ProfileID: *profile, ModelMode: mode, ModelID: model}.Normalized()
	return &s, true, s.Validate()
}

func (body llmModelSelectionRequest) selection(profileID int64) db.LLMModelSelection {
	s := db.LLMModelSelection{ProfileID: profileID, ModelMode: body.ModelMode}
	if body.ModelID != nil {
		id := int64(*body.ModelID)
		s.ModelID = &id
	}
	return s.Normalized()
}

// RegisterLLMModelRoutes 交给主路由在既有鉴权包裹内调用，不独立挂载公开入口。
func (s *Server) RegisterLLMModelRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/llm/profiles/{id}/models", s.pgGetLLMModels)
	mux.HandleFunc("PUT /api/llm/profiles/{id}/models", s.pgPutLLMModels)
	mux.HandleFunc("GET /api/llm/pool/routes", s.pgGetLLMPoolModelRoutes)
	mux.HandleFunc("PUT /api/llm/pool/routes/{id}", s.pgPutLLMPoolModelRoute)
}

func decodeLLMModelRequest(w http.ResponseWriter, r *http.Request, body any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(body); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("请求体必须包含且只包含一个 JSON 对象")
	}
	return nil
}

func llmModelProfileID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "无效渠道 ID")
		return 0, false
	}
	return id, true
}

func (s *Server) pgGetLLMModels(w http.ResponseWriter, r *http.Request) {
	id, ok := llmModelProfileID(w, r)
	if !ok {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	set, err := pg.ListLLMModels(id)
	if err != nil {
		writeLLMModelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, llmModelSetDTO(set))
}

func (s *Server) pgPutLLMModels(w http.ResponseWriter, r *http.Request) {
	id, ok := llmModelProfileID(w, r)
	if !ok {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		Models []struct {
			ID              llmModelRequestID `json:"id"`
			UpstreamModelID string            `json:"upstream_model_id"`
			DisplayName     string            `json:"display_name"`
			Enabled         bool              `json:"enabled"`
			Default         bool              `json:"is_default"`
		} `json:"models"`
	}
	if err := decodeLLMModelRequest(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效请求: "+err.Error())
		return
	}
	input := make([]db.LLMModelInput, 0, len(body.Models))
	for _, m := range body.Models {
		input = append(input, db.LLMModelInput{ID: int64(m.ID), UpstreamModelID: m.UpstreamModelID,
			DisplayName: m.DisplayName, Enabled: m.Enabled, Default: m.Default})
	}
	set, err := pg.ReplaceLLMModelsContext(r.Context(), id, input)
	if err != nil {
		writeLLMModelError(w, err)
		return
	}
	s.llmModelConfigurationChanged()
	writeJSON(w, http.StatusOK, llmModelSetDTO(set))
}

func (s *Server) pgGetLLMPoolModelRoutes(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	routes, err := pg.ListLLMPoolModelRoutes(r.Context())
	if err != nil {
		writeLLMModelError(w, err)
		return
	}
	out := make([]LLMModelSelectionDTO, 0, len(routes))
	for _, route := range routes {
		out = append(out, llmModelSelectionDTO(route))
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": out})
}

func (s *Server) pgPutLLMPoolModelRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := llmModelProfileID(w, r)
	if !ok {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body llmModelSelectionRequest
	if err := decodeLLMModelRequest(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效请求: "+err.Error())
		return
	}
	selection := body.selection(id)
	if err := pg.SaveLLMPoolModelRoute(r.Context(), selection); err != nil {
		writeLLMModelError(w, err)
		return
	}
	s.llmModelConfigurationChanged()
	writeJSON(w, http.StatusOK, llmModelSelectionDTO(selection))
}

func (s *Server) llmModelConfigurationChanged() {
	s.invalidateProfileAgents()
	if s.engine != nil {
		s.reapplyActiveProfile()
	}
}

func writeLLMModelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrLLMProfileNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, db.ErrLLMModelReferenced):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, db.ErrLLMModelInvalid), errors.Is(err, db.ErrLLMModelWrongProfile),
		errors.Is(err, db.ErrLLMModelDefaultDisabled), errors.Is(err, db.ErrLLMModelUnavailable):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		// 不回显数据库语句、连接串或参数，避免未来加入凭据参数后造成泄漏。
		writeErr(w, http.StatusInternalServerError, "模型配置操作失败")
	}
}
