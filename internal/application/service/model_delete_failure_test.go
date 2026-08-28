package service

// Task008 GAP-3: D1A model-delete cache purge wiring at service level.
// The purge hook exists in DeleteModel (best-effort DeleteByModel) but no
// service test asserts the wiring or the best-effort semantics.

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type purgeRecordingCacheRepo struct {
	interfaces.EmbeddingCacheRepository
	purgedTenant uint64
	purgedModel  string
	err          error
}

func (p *purgeRecordingCacheRepo) DeleteByModel(_ context.Context, tenantID uint64, modelID string) error {
	p.purgedTenant = tenantID
	p.purgedModel = modelID
	return p.err
}

func TestDeleteModelPurgesCacheBestEffort(t *testing.T) {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	modelID := "free-model"

	t.Run("purge called with tenant and model", func(t *testing.T) {
		cache := &purgeRecordingCacheRepo{}
		deleted := false
		svc := NewMeteredModelService(
			&stubModelRepoForDelete{
				model: &types.Model{ID: modelID, TenantID: 1},
				delete: func(id string) error {
					deleted = true
					return nil
				},
			},
			&stubKBRepoForModelDelete{},
			&stubAgentRepoForModelDelete{},
			nil, nil, nil, nil, cache,
			&config.Config{EmbeddingCache: &config.EmbeddingCacheConfig{Enabled: true}},
		)
		if err := svc.DeleteModel(ctx, modelID); err != nil {
			t.Fatalf("delete failed: %v", err)
		}
		if !deleted {
			t.Fatal("model row not deleted")
		}
		if cache.purgedTenant != 1 || cache.purgedModel != modelID {
			t.Fatalf("purge wiring wrong: tenant=%d model=%q", cache.purgedTenant, cache.purgedModel)
		}
	})

	t.Run("purge failure never blocks delete", func(t *testing.T) {
		cache := &purgeRecordingCacheRepo{err: errors.New("injected purge failure")}
		deleted := false
		svc := NewMeteredModelService(
			&stubModelRepoForDelete{
				model: &types.Model{ID: modelID, TenantID: 1},
				delete: func(id string) error {
					deleted = true
					return nil
				},
			},
			&stubKBRepoForModelDelete{},
			&stubAgentRepoForModelDelete{},
			nil, nil, nil, nil, cache,
			&config.Config{EmbeddingCache: &config.EmbeddingCacheConfig{Enabled: true}},
		)
		if err := svc.DeleteModel(ctx, modelID); err != nil {
			t.Fatalf("purge failure must not block delete: %v", err)
		}
		if !deleted {
			t.Fatal("model row not deleted despite purge failure")
		}
	})
}
