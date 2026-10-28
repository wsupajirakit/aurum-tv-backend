package channel

import (
	_ "embed"
	"encoding/json"
)

// The initial channel list, used when DATA_DIR has neither channels.json nor its backup.
//
//go:embed seed/channels.json
var seedJSON []byte

// Seed returns the embedded initial channel list (also used by aurumctl restore-channels).
func Seed() ([]Raw, error) {
	var list []Raw
	if err := json.Unmarshal(seedJSON, &list); err != nil {
		return nil, err
	}
	return list, nil
}
