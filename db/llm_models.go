package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	LLMModelModeChannelDefault = "channel-default"
	LLMModelModeFixed          = "fixed-model"
)

var (
	ErrLLMModelInvalid         = errors.New("模型选择无效")
	ErrLLMModelDefaultDisabled = errors.New("默认模型必须启用")
	ErrLLMModelWrongProfile    = errors.New("模型成员不存在或不属于该渠道")
	ErrLLMModelReferenced      = errors.New("模型成员被固定路由引用，请先修改路由")
	ErrLLMModelUnavailable     = errors.New("模型成员不存在或已禁用")
)

// LLMModel 是渠道中的稳定模型实体。展示名称不参与请求模型或路由判断。
type LLMModel struct {
	ID              int64  `json:"id"`
	ProfileID       int64  `json:"profile_id"`
	UpstreamModelID string `json:"upstream_model_id"`
	DisplayName     string `json:"display_name"`
	Enabled         bool   `json:"enabled"`
	Default         bool   `json:"is_default"`
}

type LLMModelInput struct {
	ID              int64  `json:"id"`
	UpstreamModelID string `json:"upstream_model_id"`
	DisplayName     string `json:"display_name"`
	Enabled         bool   `json:"enabled"`
	Default         bool   `json:"is_default"`
}

type LLMModelSet struct {
	ProfileID      int64      `json:"profile_id"`
	DefaultModelID *int64     `json:"default_model_id"`
	Models         []LLMModel `json:"models"`
}

// LLMModelSelection 记录选择方式；渠道默认模式不保存当时解析出的成员 ID。
type LLMModelSelection struct {
	ProfileID int64  `json:"profile_id"`
	ModelMode string `json:"model_mode"`
	ModelID   *int64 `json:"model_id"`
}

// Validate 只检查选择形状，成员归属与启用状态由数据库解析检查。
func (s LLMModelSelection) Validate() error {
	if s.ProfileID <= 0 {
		return ErrLLMModelInvalid
	}
	switch s.ModelMode {
	case "", LLMModelModeChannelDefault:
		if s.ModelID != nil {
			return ErrLLMModelInvalid
		}
	case LLMModelModeFixed:
		if s.ModelID == nil || *s.ModelID <= 0 {
			return ErrLLMModelInvalid
		}
	default:
		return ErrLLMModelInvalid
	}
	return nil
}

func (s LLMModelSelection) Normalized() LLMModelSelection {
	if s.ModelMode == "" {
		s.ModelMode = LLMModelModeChannelDefault
	}
	return s
}

// MemberKey 按实际成员身份隔离缓存、轮询和熔断，展示名称不在身份中。
func (m LLMModel) MemberKey() string { return fmt.Sprintf("%d:%d", m.ProfileID, m.ID) }

// MigrateLLMModels 不依赖种子数据。独立事务与事务级锁保证多实例启动可重复执行。
func (d *DB) MigrateLLMModels(ctx context.Context) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7337741003)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS llm_models (
    id BIGSERIAL PRIMARY KEY,
    profile_id BIGINT NOT NULL REFERENCES llm_profiles(id) ON DELETE CASCADE,
    upstream_model_id TEXT NOT NULL CHECK (btrim(upstream_model_id) <> ''),
    display_name TEXT NOT NULL CHECK (btrim(display_name) <> ''),
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (profile_id, upstream_model_id),
    UNIQUE (profile_id, id)
);
ALTER TABLE llm_profiles ADD COLUMN IF NOT EXISTS default_model_id BIGINT REFERENCES llm_models(id) ON DELETE SET NULL;
CREATE TABLE IF NOT EXISTS llm_pool_model_routes (
    profile_id BIGINT PRIMARY KEY REFERENCES llm_profiles(id) ON DELETE CASCADE,
    model_mode TEXT NOT NULL CHECK (model_mode IN ('channel-default','fixed-model')),
    model_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (profile_id, model_id) REFERENCES llm_models(profile_id, id),
    CHECK ((model_mode='channel-default' AND model_id IS NULL)
        OR (model_mode='fixed-model' AND model_id IS NOT NULL))
);
INSERT INTO llm_models(profile_id,upstream_model_id,display_name,enabled)
SELECT id,model,model,true FROM llm_profiles
WHERE default_model_id IS NULL AND btrim(model) <> ''
ON CONFLICT (profile_id,upstream_model_id) DO NOTHING;
UPDATE llm_profiles p SET default_model_id=m.id
FROM llm_models m
WHERE p.default_model_id IS NULL AND m.profile_id=p.id AND m.upstream_model_id=p.model AND m.enabled;
UPDATE llm_profiles p SET model=m.upstream_model_id
FROM llm_models m
WHERE p.default_model_id=m.id AND m.profile_id=p.id AND p.model<>m.upstream_model_id;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS active_llm_position INTEGER;
ALTER TABLE task_llm_profiles ADD COLUMN IF NOT EXISTS model_mode TEXT NOT NULL DEFAULT 'channel-default';
ALTER TABLE task_llm_profiles ADD COLUMN IF NOT EXISTS model_id BIGINT;
ALTER TABLE task_llm_profiles ADD COLUMN IF NOT EXISTS exhausted_model_id BIGINT;
ALTER TABLE task_llm_profiles DROP CONSTRAINT IF EXISTS task_llm_profiles_pkey;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='task_llm_models_pkey') THEN
  ALTER TABLE task_llm_profiles ADD CONSTRAINT task_llm_models_pkey PRIMARY KEY(task_id,position);
 END IF;
END $$;
UPDATE tasks t SET active_llm_position=p.position FROM task_llm_profiles p
WHERE t.active_llm_position IS NULL AND t.active_llm_profile_id=p.profile_id AND p.task_id=t.id;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS llm_model_mode TEXT NOT NULL DEFAULT 'channel-default';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS llm_model_id BIGINT;
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS llm_model_mode TEXT NOT NULL DEFAULT 'channel-default';
ALTER TABLE conversations ADD COLUMN IF NOT EXISTS llm_model_id BIGINT;
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='task_llm_models_reference') THEN
  ALTER TABLE task_llm_profiles ADD CONSTRAINT task_llm_models_reference FOREIGN KEY(profile_id,model_id) REFERENCES llm_models(profile_id,id);
  ALTER TABLE task_llm_profiles ADD CONSTRAINT task_llm_models_selection CHECK ((model_mode='channel-default' AND model_id IS NULL) OR (model_mode='fixed-model' AND model_id IS NOT NULL));
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='agents_llm_model_reference') THEN
  ALTER TABLE agents ADD CONSTRAINT agents_llm_model_reference FOREIGN KEY(llm_profile_id,llm_model_id) REFERENCES llm_models(profile_id,id);
  ALTER TABLE agents ADD CONSTRAINT agents_llm_model_selection CHECK ((llm_model_mode='channel-default' AND llm_model_id IS NULL) OR (llm_model_mode='fixed-model' AND llm_model_id IS NOT NULL AND llm_profile_id IS NOT NULL));
 END IF;
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='conversations_llm_model_reference') THEN
  ALTER TABLE conversations ADD CONSTRAINT conversations_llm_model_reference FOREIGN KEY(llm_profile_id,llm_model_id) REFERENCES llm_models(profile_id,id);
  ALTER TABLE conversations ADD CONSTRAINT conversations_llm_model_selection CHECK ((llm_model_mode='channel-default' AND llm_model_id IS NULL) OR (llm_model_mode='fixed-model' AND llm_model_id IS NOT NULL AND llm_profile_id IS NOT NULL));
 END IF;
END $$;
CREATE TABLE IF NOT EXISTS llm_model_health (
 profile_id BIGINT NOT NULL, model_id BIGINT NOT NULL,
 fails INTEGER NOT NULL DEFAULT 0, trips INTEGER NOT NULL DEFAULT 0,
 open_until TIMESTAMPTZ, last_error TEXT, last_at TIMESTAMPTZ,
 PRIMARY KEY(profile_id,model_id),
 FOREIGN KEY(profile_id,model_id) REFERENCES llm_models(profile_id,id) ON DELETE CASCADE
);
INSERT INTO llm_model_health(profile_id,model_id,fails,trips,open_until,last_error,last_at)
SELECT h.profile_id,p.default_model_id,h.fails,h.trips,h.open_until,h.last_error,h.last_at
FROM llm_profile_health h JOIN llm_profiles p ON p.id=h.profile_id WHERE p.default_model_id IS NOT NULL
ON CONFLICT DO NOTHING;
DELETE FROM llm_profile_health h USING llm_profiles p
WHERE h.profile_id=p.id AND p.default_model_id IS NOT NULL;
`); err != nil {
		return fmt.Errorf("迁移渠道模型: %w", err)
	}
	return tx.Commit()
}

type llmModelQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (d *DB) ListLLMModels(profileID int64) (LLMModelSet, error) {
	return listLLMModels(context.Background(), d, profileID)
}

// 单条 LEFT JOIN 保证返回的默认成员和列表来自同一个数据库快照。
func listLLMModels(ctx context.Context, q llmModelQueryer, profileID int64) (LLMModelSet, error) {
	set := LLMModelSet{ProfileID: profileID, Models: []LLMModel{}}
	rows, err := q.QueryContext(ctx, `SELECT p.default_model_id,m.id,m.upstream_model_id,m.display_name,m.enabled
FROM llm_profiles p LEFT JOIN llm_models m ON m.profile_id=p.id WHERE p.id=$1 ORDER BY m.id`, profileID)
	if err != nil {
		return set, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		found = true
		var def, id sql.NullInt64
		var upstream, display sql.NullString
		var enabled sql.NullBool
		if err := rows.Scan(&def, &id, &upstream, &display, &enabled); err != nil {
			return set, err
		}
		if def.Valid {
			value := def.Int64
			set.DefaultModelID = &value
		}
		if id.Valid {
			set.Models = append(set.Models, LLMModel{ID: id.Int64, ProfileID: profileID,
				UpstreamModelID: upstream.String, DisplayName: display.String, Enabled: enabled.Bool,
				Default: def.Valid && def.Int64 == id.Int64})
		}
	}
	if err := rows.Err(); err != nil {
		return set, err
	}
	if !found {
		return set, ErrLLMProfileNotFound
	}
	return set, nil
}

func validateLLMModels(in []LLMModelInput) ([]LLMModelInput, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: 至少保留一个模型", ErrLLMModelInvalid)
	}
	out := append([]LLMModelInput(nil), in...)
	upstreams, ids := map[string]bool{}, map[int64]bool{}
	defaults := 0
	for i := range out {
		m := &out[i]
		m.UpstreamModelID = strings.TrimSpace(m.UpstreamModelID)
		m.DisplayName = strings.TrimSpace(m.DisplayName)
		if m.ID < 0 || m.UpstreamModelID == "" || m.DisplayName == "" || upstreams[m.UpstreamModelID] || (m.ID > 0 && ids[m.ID]) {
			return nil, fmt.Errorf("%w: ID、上游 ID 和展示名称必须有效且上游 ID 不重复", ErrLLMModelInvalid)
		}
		upstreams[m.UpstreamModelID], ids[m.ID] = true, true
		if m.Default {
			defaults++
			if !m.Enabled {
				return nil, ErrLLMModelDefaultDisabled
			}
		}
	}
	if defaults != 1 {
		return nil, fmt.Errorf("%w: 必须且只能选择一个默认模型", ErrLLMModelInvalid)
	}
	return out, nil
}

func (d *DB) ReplaceLLMModels(profileID int64, in []LLMModelInput) (LLMModelSet, error) {
	return d.ReplaceLLMModelsContext(context.Background(), profileID, in)
}

func (d *DB) ReplaceLLMModelsContext(ctx context.Context, profileID int64, in []LLMModelInput) (LLMModelSet, error) {
	models, err := validateLLMModels(in)
	if err != nil {
		return LLMModelSet{}, err
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return LLMModelSet{}, err
	}
	defer tx.Rollback()
	var lockedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM llm_profiles WHERE id=$1 FOR UPDATE`, profileID).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LLMModelSet{}, ErrLLMProfileNotFound
		}
		return LLMModelSet{}, err
	}
	existing, err := listLLMModels(ctx, tx, profileID)
	if err != nil {
		return LLMModelSet{}, err
	}
	byID, byUpstream := map[int64]LLMModel{}, map[string]LLMModel{}
	for _, m := range existing.Models {
		byID[m.ID], byUpstream[m.UpstreamModelID] = m, m
	}
	kept := map[int64]bool{}
	var defaultID int64
	var defaultUpstream string
	for _, m := range models {
		if m.ID > 0 {
			old, ok := byID[m.ID]
			if !ok {
				return LLMModelSet{}, ErrLLMModelWrongProfile
			}
			if old.UpstreamModelID != m.UpstreamModelID {
				return LLMModelSet{}, fmt.Errorf("%w: 已有成员的上游 ID 不可修改，请添加新成员", ErrLLMModelInvalid)
			}
		} else if old, ok := byUpstream[m.UpstreamModelID]; ok {
			m.ID = old.ID
		}
		if m.ID == 0 {
			err = tx.QueryRowContext(ctx, `INSERT INTO llm_models(profile_id,upstream_model_id,display_name,enabled)
VALUES($1,$2,$3,$4) RETURNING id`, profileID, m.UpstreamModelID, m.DisplayName, m.Enabled).Scan(&m.ID)
		} else {
			if !m.Enabled {
				var referenced bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM llm_pool_model_routes WHERE model_id=$1
UNION ALL SELECT 1 FROM task_llm_profiles WHERE model_id=$1
UNION ALL SELECT 1 FROM agents WHERE llm_model_id=$1
UNION ALL SELECT 1 FROM conversations WHERE llm_model_id=$1)`, m.ID).Scan(&referenced); err != nil {
					return LLMModelSet{}, err
				}
				if referenced {
					return LLMModelSet{}, ErrLLMModelReferenced
				}
			}
			_, err = tx.ExecContext(ctx, `UPDATE llm_models SET display_name=$1,enabled=$2,updated_at=now() WHERE id=$3 AND profile_id=$4`, m.DisplayName, m.Enabled, m.ID, profileID)
		}
		if err != nil {
			return LLMModelSet{}, llmModelWriteError(err)
		}
		kept[m.ID] = true
		if m.Default {
			defaultID, defaultUpstream = m.ID, m.UpstreamModelID
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_profiles SET default_model_id=$1,model=$2 WHERE id=$3`, defaultID, defaultUpstream, profileID); err != nil {
		return LLMModelSet{}, err
	}
	for _, m := range existing.Models {
		if !kept[m.ID] {
			if _, err := tx.ExecContext(ctx, `DELETE FROM llm_models WHERE id=$1`, m.ID); err != nil {
				return LLMModelSet{}, llmModelWriteError(err)
			}
		}
	}
	result, err := listLLMModels(ctx, tx, profileID)
	if err != nil {
		return LLMModelSet{}, err
	}
	if err := tx.Commit(); err != nil {
		return LLMModelSet{}, llmModelWriteError(err)
	}
	return result, nil
}

func llmModelWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%w", ErrLLMModelReferenced)
	}
	return err
}

// ResolveLLMModel 每次按保存的选择解析，不使用展示名称匹配或降级。
func (d *DB) ResolveLLMModel(ctx context.Context, selection LLMModelSelection) (*LLMModel, error) {
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	set, err := listLLMModels(ctx, d, selection.ProfileID)
	if err != nil {
		return nil, err
	}
	return ResolveLLMModelSelection(selection, set)
}

// ResolveLLMModelSelection 同时供运行解析与纯单元测试使用。
func ResolveLLMModelSelection(selection LLMModelSelection, set LLMModelSet) (*LLMModel, error) {
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	if selection.ProfileID != set.ProfileID {
		return nil, ErrLLMModelWrongProfile
	}
	id := selection.ModelID
	if selection.Normalized().ModelMode == LLMModelModeChannelDefault {
		id = set.DefaultModelID
	}
	if id == nil {
		return nil, ErrLLMModelUnavailable
	}
	for _, model := range set.Models {
		if model.ID == *id && model.ProfileID == selection.ProfileID {
			if !model.Enabled {
				return nil, ErrLLMModelUnavailable
			}
			return &model, nil
		}
	}
	return nil, ErrLLMModelUnavailable
}

func (d *DB) ListLLMPoolModelRoutes(ctx context.Context) ([]LLMModelSelection, error) {
	rows, err := d.QueryContext(ctx, `SELECT p.id,COALESCE(r.model_mode,'channel-default'),r.model_id
FROM llm_profiles p LEFT JOIN llm_pool_model_routes r ON r.profile_id=p.id ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LLMModelSelection{}
	for rows.Next() {
		var selection LLMModelSelection
		if err := rows.Scan(&selection.ProfileID, &selection.ModelMode, &selection.ModelID); err != nil {
			return nil, err
		}
		out = append(out, selection)
	}
	return out, rows.Err()
}

func (d *DB) SaveLLMPoolModelRoute(ctx context.Context, selection LLMModelSelection) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	selection = selection.Normalized()
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var profileID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM llm_profiles WHERE id=$1 FOR UPDATE`, selection.ProfileID).Scan(&profileID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLLMProfileNotFound
		}
		return err
	}
	set, err := listLLMModels(ctx, tx, selection.ProfileID)
	if err != nil {
		return err
	}
	if _, err := ResolveLLMModelSelection(selection, set); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_pool_model_routes(profile_id,model_mode,model_id)
VALUES($1,$2,$3) ON CONFLICT(profile_id) DO UPDATE SET model_mode=EXCLUDED.model_mode,model_id=EXCLUDED.model_id,updated_at=now()`,
		selection.ProfileID, selection.ModelMode, selection.ModelID); err != nil {
		return err
	}
	return tx.Commit()
}

func selectionsFromProfiles(ids []int64) []LLMModelSelection {
	var out []LLMModelSelection
	for _, id := range ids {
		out = append(out, LLMModelSelection{ProfileID: id, ModelMode: LLMModelModeChannelDefault})
	}
	return out
}

func renumberTaskLLMMembers(ctx context.Context, tx *sql.Tx, taskID int64) error {
	var offset int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(position),0)+1 FROM task_llm_profiles WHERE task_id=$1`, taskID).Scan(&offset); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET active_llm_position=(SELECT count(*)-1 FROM task_llm_profiles WHERE task_id=$1 AND position<=tasks.active_llm_position) WHERE id=$1 AND active_llm_position IS NOT NULL`, taskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_llm_profiles SET position=position+$2 WHERE task_id=$1`, taskID, offset); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `WITH numbered AS (SELECT position,row_number() OVER(ORDER BY position)-1 AS new_position FROM task_llm_profiles WHERE task_id=$1)
UPDATE task_llm_profiles p SET position=n.new_position FROM numbered n WHERE p.task_id=$1 AND p.position=n.position`, taskID)
	return err
}

func selectionColumns(s *LLMModelSelection) (any, string, any) {
	if s == nil {
		return nil, LLMModelModeChannelDefault, nil
	}
	return s.ProfileID, s.Normalized().ModelMode, s.ModelID
}

// 先锁引用行，再锁渠道；与删除渠道保持同一顺序，并阻止禁用检查和新增引用交错。
func lockLLMSelection(tx *sql.Tx, s *LLMModelSelection) error {
	if s == nil {
		return nil
	}
	if err := s.Validate(); err != nil {
		return err
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM llm_profiles WHERE id=$1 FOR SHARE`, s.ProfileID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLLMProfileNotFound
		}
		return err
	}
	set, err := listLLMModels(context.Background(), tx, s.ProfileID)
	if err != nil {
		return err
	}
	_, err = ResolveLLMModelSelection(*s, set)
	return err
}

func insertTaskLLMMembers(tx *sql.Tx, taskID int64, members []LLMModelSelection) error {
	if len(members) > 64 {
		return fmt.Errorf("%w: 候选最多64项", ErrLLMModelInvalid)
	}
	seen := map[string]bool{}
	// 渠道锁按 ID 排序，避免两条相反候选顺序形成锁环。
	profileIDs := make([]int64, 0, len(members))
	for _, s := range members {
		if err := s.Validate(); err != nil {
			return err
		}
		profileIDs = append(profileIDs, s.ProfileID)
	}
	rows, err := tx.Query(`SELECT id FROM llm_profiles WHERE id=ANY($1::bigint[]) ORDER BY id FOR SHARE`, profileIDs)
	if err != nil {
		return err
	}
	rows.Close()
	for position, s := range members {
		s = s.Normalized()
		if err := lockLLMSelection(tx, &s); err != nil {
			return err
		}
		key := fmt.Sprintf("%d:%s", s.ProfileID, s.ModelMode)
		if s.ModelID != nil {
			key += fmt.Sprintf(":%d", *s.ModelID)
		}
		if seen[key] {
			return fmt.Errorf("%w: 重复候选", ErrLLMModelInvalid)
		}
		seen[key] = true
		if _, err := tx.Exec(`INSERT INTO task_llm_profiles(task_id,profile_id,position,model_mode,model_id) VALUES($1,$2,$3,$4,$5)`, taskID, s.ProfileID, position, s.ModelMode, s.ModelID); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) ReplaceTaskLLMMembers(taskID int64, members []LLMModelSelection, activePosition *int) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`SELECT id FROM tasks WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, taskID).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM task_llm_profiles WHERE task_id=$1`, taskID); err != nil {
		return err
	}
	if err := insertTaskLLMMembers(tx, taskID, members); err != nil {
		return err
	}
	var profile, position any
	if len(members) > 0 {
		pos := 0
		if activePosition != nil {
			pos = *activePosition
		}
		if pos < 0 || pos >= len(members) {
			return ErrLLMModelInvalid
		}
		profile, position = members[pos].ProfileID, pos
	}
	if _, err := tx.Exec(`UPDATE tasks SET llm_profile_id=$2,active_llm_profile_id=$2,active_llm_position=$3,llm_chain_revision=llm_chain_revision+1 WHERE id=$1`, taskID, profile, position); err != nil {
		return err
	}
	return tx.Commit()
}

// 调用开始时的实际成员和链版本缺一不可；默认切换不应耗尽新的成员。
func (d *DB) MarkTaskLLMModelQuotaExhausted(taskID int64, position int, modelID, revision int64, reason string) (TaskLLMTransition, error) {
	var out TaskLLMTransition
	tx, err := d.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var active *int
	var currentRevision int64
	if err := tx.QueryRow(`SELECT active_llm_position,llm_chain_revision FROM tasks WHERE id=$1 FOR UPDATE`, taskID).Scan(&active, &currentRevision); err != nil {
		return out, err
	}
	if revision != currentRevision {
		out.Stale = true
		return out, tx.Commit()
	}
	var selection LLMModelSelection
	if err := tx.QueryRow(`SELECT profile_id,model_mode,model_id FROM task_llm_profiles WHERE task_id=$1 AND position=$2`, taskID, position).Scan(&selection.ProfileID, &selection.ModelMode, &selection.ModelID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			out.Stale = true
			return out, tx.Commit()
		}
		return out, err
	}
	out.PreviousProfileID = selection.ProfileID
	out.PreviousModelID = modelID
	if err := lockLLMSelection(tx, &selection); err != nil {
		return out, err
	}
	set, err := listLLMModels(context.Background(), tx, selection.ProfileID)
	if err != nil {
		return out, err
	}
	actual, err := ResolveLLMModelSelection(selection, set)
	if err != nil {
		return out, err
	}
	if actual.ID != modelID {
		out.Stale = true
		return out, tx.Commit()
	}
	if active == nil {
		out.ChainExhausted = true
		return out, tx.Commit()
	}
	if _, err := tx.Exec(`UPDATE task_llm_profiles SET status='quota_exhausted',last_error=$3,exhausted_at=now(),exhausted_model_id=$4 WHERE task_id=$1 AND position=$2`, taskID, position, truncateUTF8(strings.TrimSpace(reason), 1000), modelID); err != nil {
		return out, err
	}
	if *active != position {
		return out, tx.Commit()
	}
	var nextPosition int
	var nextProfile int64
	err = tx.QueryRow(`SELECT x.position,x.profile_id FROM task_llm_profiles x JOIN llm_profiles p ON p.id=x.profile_id
WHERE x.task_id=$1 AND x.position>$2 AND (x.status='ready' OR (x.model_mode='channel-default' AND x.exhausted_model_id IS DISTINCT FROM p.default_model_id)) ORDER BY x.position LIMIT 1`, taskID, position).Scan(&nextPosition, &nextProfile)
	var cursor, profile any
	if err == nil {
		cursor, profile = nextPosition, nextProfile
		out.NextPosition = &nextPosition
		out.NextProfileID = &nextProfile
	} else if errors.Is(err, sql.ErrNoRows) {
		out.ChainExhausted = true
	} else {
		return out, err
	}
	out.Advanced = true
	if _, err := tx.Exec(`UPDATE tasks SET active_llm_position=$2,active_llm_profile_id=$3,llm_profile_id=$3,llm_chain_revision=llm_chain_revision+1 WHERE id=$1`, taskID, cursor, profile); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
