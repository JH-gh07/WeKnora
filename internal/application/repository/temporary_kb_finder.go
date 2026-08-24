package repository

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// temporaryKnowledgeBaseFinder implements interfaces.TemporaryKnowledgeBaseFinder.
// It is a deliberately narrow adapter so evaluation-run reconciliation does not
// have to depend on the full KnowledgeBaseRepository interface.
type temporaryKnowledgeBaseFinder struct {
	db *gorm.DB
}

// NewTemporaryKnowledgeBaseFinder creates the temporary KB locator.
func NewTemporaryKnowledgeBaseFinder(db *gorm.DB) interfaces.TemporaryKnowledgeBaseFinder {
	return &temporaryKnowledgeBaseFinder{db: db}
}

// GetTemporaryKnowledgeBaseByResourceKey finds a temporary KB by its resource
// locator (Description) scoped to the tenant.
func (r *temporaryKnowledgeBaseFinder) GetTemporaryKnowledgeBaseByResourceKey(
	ctx context.Context, tenantID uint64, resourceKey string,
) (*types.KnowledgeBase, error) {
	var kb types.KnowledgeBase
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND is_temporary = ? AND description = ?", tenantID, true, resourceKey).
		First(&kb).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrKnowledgeBaseNotFound
	}
	if err != nil {
		return nil, err
	}
	return &kb, nil
}
