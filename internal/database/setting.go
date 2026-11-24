package database

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"

	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/setting"
)

// LoadSetting decodes one JSON settings row. The second result reports
// whether the key exists; a missing key is not an error.
func LoadSetting[T any](ctx context.Context, client *ent.Client, key string) (T, bool, error) {
	var value T
	record, err := client.Setting.Query().Where(setting.Key(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return value, false, nil
	}
	if err != nil {
		return value, false, fmt.Errorf("load setting %s: %w", key, err)
	}
	if err := json.Unmarshal(record.Value, &value); err != nil {
		return value, false, fmt.Errorf("decode setting %s: %w", key, err)
	}
	return value, true, nil
}

// SaveSetting upserts one JSON settings row.
func SaveSetting(ctx context.Context, client *ent.Client, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode setting %s: %w", key, err)
	}
	if err := client.Setting.Create().SetKey(key).SetValue(jsontext.Value(encoded)).
		OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx); err != nil {
		return fmt.Errorf("save setting %s: %w", key, err)
	}
	return nil
}

// DeleteSetting removes one settings row. A missing row is not an error.
func DeleteSetting(ctx context.Context, client *ent.Client, key string) error {
	if _, err := client.Setting.Delete().Where(setting.Key(key)).Exec(ctx); err != nil {
		return fmt.Errorf("delete setting %s: %w", key, err)
	}
	return nil
}
