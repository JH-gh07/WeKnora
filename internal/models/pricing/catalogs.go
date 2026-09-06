package pricing

import (
	_ "embed"
	"fmt"
)

//go:embed catalogs/siliconflow.json
var siliconflowCatalogJSON []byte

// LoadDefaultCatalog loads the compiled-in SiliconFlow pricing catalog. It runs
// the full validator, so a corrupt or future-invalid catalog fails fast at
// wiring time rather than silently producing wrong amounts at request time.
func LoadDefaultCatalog() (*LoadedCatalog, error) {
	c, err := LoadCatalog(siliconflowCatalogJSON)
	if err != nil {
		return nil, fmt.Errorf("pricing: load default catalog: %w", err)
	}
	return c, nil
}
