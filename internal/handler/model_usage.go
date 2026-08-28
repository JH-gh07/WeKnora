package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type ModelUsageHandler struct {
	service interfaces.ModelUsageService
	now     func() time.Time
}

func NewModelUsageHandler(service interfaces.ModelUsageService) *ModelUsageHandler {
	return &ModelUsageHandler{service: service, now: time.Now}
}

func (h *ModelUsageHandler) Aggregate(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("Unauthorized"))
		return
	}
	from, to, err := h.parseWindow(c)
	if err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	filter := types.ModelCallFilter{TenantID: tenantID, ModelID: strings.TrimSpace(c.Query("model_id")), Operation: types.ModelOperation(strings.TrimSpace(c.Query("operation"))), Purpose: strings.TrimSpace(c.Query("purpose")), From: &from, To: &to}
	if raw := strings.TrimSpace(c.Query("run_id")); raw != "" {
		filter.RunID = &raw
	}
	if filter.Operation != "" && filter.Operation != types.ModelOperationChat && filter.Operation != types.ModelOperationEmbedding && filter.Operation != types.ModelOperationRerank {
		c.Error(errors.NewBadRequestError("operation must be chat, embedding, or rerank"))
		return
	}
	result, err := h.service.Aggregate(c.Request.Context(), filter)
	if err != nil {
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func (h *ModelUsageHandler) Health(c *gin.Context) {
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	if tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("Unauthorized"))
		return
	}
	from, to, err := h.parseWindow(c)
	if err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	result, err := h.service.Health(c.Request.Context(), tenantID, from, to)
	if err != nil {
		c.Error(errors.NewInternalServerError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func (h *ModelUsageHandler) parseWindow(c *gin.Context) (time.Time, time.Time, error) {
	to := h.now().UTC()
	from := to.Add(-24 * time.Hour)
	var err error
	if raw := strings.TrimSpace(c.Query("from")); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("from must be RFC3339")
		}
	}
	if raw := strings.TrimSpace(c.Query("to")); raw != "" {
		to, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("to must be RFC3339")
		}
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be before to")
	}
	return from.UTC(), to.UTC(), nil
}
