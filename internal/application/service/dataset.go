package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/parquet-go/parquet-go"
)

// DatasetService provides operations for working with datasets.
type DatasetService struct {
	registry *datasetRegistry
}

// NewDatasetService creates a DatasetService backed by the config registry.
func NewDatasetService() interfaces.DatasetService {
	return &DatasetService{registry: newDatasetRegistry(defaultDatasetBaseDir())}
}

// TextInfo represents text data with ID in parquet format.
type TextInfo struct {
	ID   int64  `parquet:"id"`   // Unique identifier
	Text string `parquet:"text"` // Text content
}

// RelsInfo represents question-passage relations in parquet format.
type RelsInfo struct {
	QID int64 `parquet:"qid"` // Question ID
	PID int64 `parquet:"pid"` // Passage ID
}

// QaInfo represents question-answer relations in parquet format.
type QaInfo struct {
	QID int64 `parquet:"qid"` // Question ID
	AID int64 `parquet:"aid"` // Answer ID
}

// GetDatasetByID retrieves QA pairs from the dataset resolved by datasetID.
// An unknown datasetID returns ErrDatasetNotFound (typed error) and never falls
// back to the default dataset (I01).
func (d *DatasetService) GetDatasetByID(ctx context.Context, datasetID string) ([]*types.QAPair, error) {
	logger.Infof(ctx, "Getting dataset with ID: %s", datasetID)
	return d.registry.load(ctx, datasetID)
}

// loadParquet loads data from a parquet file into the specified type.
func loadParquet[T any](filePath string) ([]T, error) {
	rows, err := parquet.ReadFile[T](filePath)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
