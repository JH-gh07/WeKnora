package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Task005 review fix — tenant-delete purge acceptance tests. They prove the
// fail-closed hook added in DeleteTenant: purge succeeds before the tenant is
// deleted, a purge failure blocks the delete, only the target tenant is
// purged, and a later tenant-delete failure still propagates.

func newTenantPurgeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.TenantMember{},
		&types.StorageBackend{},
		&types.EmbeddingCacheEntry{},
		&types.EmbeddingCacheObservation{},
	))
	return db
}

func insertCacheRows(t *testing.T, db *gorm.DB, tenantID uint64) {
	t.Helper()
	require.NoError(t, db.Create(&types.EmbeddingCacheEntry{
		ID: uuid.NewString(), TenantID: tenantID, CacheKey: "k1", ModelID: "model-a",
		ProviderIdentity: "remote|openai|https://api.openai.com/v1",
		VectorPayload:    "[1,2,3]", Dimensions: 3, CacheSchemaVersion: 1,
	}).Error)
	require.NoError(t, db.Create(&types.EmbeddingCacheObservation{
		ID: uuid.NewString(), TenantID: tenantID, ModelID: "model-a", Operation: "embedding",
		CacheMode: types.EmbeddingCacheModeOn, LogicalEmbeddingItemCount: 3,
		PersistenceStatus: types.EmbeddingCachePersistencePersisted,
	}).Error)
}

func tenantCount(t *testing.T, db *gorm.DB, id uint64) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&types.Tenant{}).Where("id = ?", id).Count(&n).Error)
	return n
}

func cacheEntryCount(t *testing.T, db *gorm.DB, tenantID uint64) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&types.EmbeddingCacheEntry{}).Where("tenant_id = ?", tenantID).Count(&n).Error)
	return n
}

func cacheObservationCount(t *testing.T, db *gorm.DB, tenantID uint64) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&types.EmbeddingCacheObservation{}).Where("tenant_id = ?", tenantID).Count(&n).Error)
	return n
}

func newTenantSvc(t *testing.T, db *gorm.DB, cache interfaces.EmbeddingCacheRepository) (interfaces.TenantService, interfaces.TenantRepository) {
	t.Helper()
	tenantRepo := repository.NewTenantRepository(db)
	storageRepo := repository.NewStorageBackendRepository(db)
	return service.NewTenantServiceWithEmbeddingCache(tenantRepo, storageRepo, cache), tenantRepo
}

func createTenant(t *testing.T, svc interfaces.TenantService, name string) *types.Tenant {
	t.Helper()
	tenant, err := svc.CreateTenant(context.Background(), &types.Tenant{Name: name})
	require.NoError(t, err)
	require.NotNil(t, tenant)
	return tenant
}

// failingPurgeCache wraps a real repository but fails DeleteByTenant, modelling
// an unavailable store at purge time.
type failingPurgeCache struct {
	interfaces.EmbeddingCacheRepository
	err error
}

func (f *failingPurgeCache) DeleteByTenant(ctx context.Context, id uint64) error {
	if f.err != nil {
		return f.err
	}
	return f.EmbeddingCacheRepository.DeleteByTenant(ctx, id)
}

// failingTenantRepo wraps a real tenant repository but fails DeleteTenant.
type failingTenantRepo struct {
	interfaces.TenantRepository
	err error
}

func (f *failingTenantRepo) DeleteTenant(ctx context.Context, id uint64) error {
	if f.err != nil {
		return f.err
	}
	return f.TenantRepository.DeleteTenant(ctx, id)
}

func TestDeleteTenantPurgesCacheBeforeTenantDelete(t *testing.T) {
	db := newTenantPurgeDB(t)
	cache := repository.NewEmbeddingCacheRepository(db)
	svc, _ := newTenantSvc(t, db, cache)

	tenant := createTenant(t, svc, "workspace-a")
	insertCacheRows(t, db, tenant.ID)
	require.Equal(t, int64(1), cacheEntryCount(t, db, tenant.ID))
	require.Equal(t, int64(1), cacheObservationCount(t, db, tenant.ID))

	require.NoError(t, svc.DeleteTenant(context.Background(), tenant.ID))
	require.Equal(t, int64(0), tenantCount(t, db, tenant.ID))
	require.Equal(t, int64(0), cacheEntryCount(t, db, tenant.ID))
	require.Equal(t, int64(0), cacheObservationCount(t, db, tenant.ID))
}

func TestDeleteTenantPurgeFailureBlocksTenantDelete(t *testing.T) {
	db := newTenantPurgeDB(t)
	real := repository.NewEmbeddingCacheRepository(db)
	cache := &failingPurgeCache{EmbeddingCacheRepository: real, err: errors.New("store down")}
	svc, _ := newTenantSvc(t, db, cache)

	tenant := createTenant(t, svc, "workspace-a")
	insertCacheRows(t, db, tenant.ID)

	err := svc.DeleteTenant(context.Background(), tenant.ID)
	require.Error(t, err)
	require.Equal(t, int64(1), tenantCount(t, db, tenant.ID), "purge failure must leave the tenant intact")
	require.Equal(t, int64(1), cacheEntryCount(t, db, tenant.ID), "purge failure must not have deleted cache rows")
	require.Equal(t, int64(1), cacheObservationCount(t, db, tenant.ID), "purge failure must not have deleted observations")
}

func TestDeleteTenantPurgeIsTenantScoped(t *testing.T) {
	db := newTenantPurgeDB(t)
	cache := repository.NewEmbeddingCacheRepository(db)
	svc, _ := newTenantSvc(t, db, cache)

	a := createTenant(t, svc, "workspace-a")
	b := createTenant(t, svc, "workspace-b")
	insertCacheRows(t, db, a.ID)
	insertCacheRows(t, db, b.ID)

	require.NoError(t, svc.DeleteTenant(context.Background(), a.ID))
	require.Equal(t, int64(0), cacheEntryCount(t, db, a.ID), "target tenant cache must be purged")
	require.Equal(t, int64(1), cacheEntryCount(t, db, b.ID), "other tenant cache must remain untouched")
	require.Equal(t, int64(0), cacheObservationCount(t, db, a.ID), "target tenant observations must be purged")
	require.Equal(t, int64(1), cacheObservationCount(t, db, b.ID), "other tenant observations must remain untouched")
}

func TestDeleteTenantRepoDeleteFailurePropagates(t *testing.T) {
	db := newTenantPurgeDB(t)
	cache := repository.NewEmbeddingCacheRepository(db)
	realRepo := repository.NewTenantRepository(db)
	repo := &failingTenantRepo{TenantRepository: realRepo, err: errors.New("tenant delete down")}
	storageRepo := repository.NewStorageBackendRepository(db)
	svc := service.NewTenantServiceWithEmbeddingCache(repo, storageRepo, cache)

	tenant := createTenant(t, svc, "workspace-a")
	insertCacheRows(t, db, tenant.ID)

	err := svc.DeleteTenant(context.Background(), tenant.ID)
	require.Error(t, err)
	// Purge runs first and is documented as harmless to have succeeded even
	// though the tenant delete then failed; the tenant itself must remain.
	require.Equal(t, int64(1), tenantCount(t, db, tenant.ID), "failed tenant delete must leave the tenant intact")
	require.Equal(t, int64(0), cacheEntryCount(t, db, tenant.ID), "purge ran before the (failed) tenant delete")
	require.Equal(t, int64(0), cacheObservationCount(t, db, tenant.ID), "observation purge ran before the failed tenant delete")
}
