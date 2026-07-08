package xray_util

import (
	"encoding/json"
	"errors"
)

var ErrDeprecatedKcpHeaderSeed = errors.New("Xray 26 已移除 mKCP header/seed")

func StripDeprecatedKcpHeaderSeed(streamSettings string) (string, bool, error) {
	if streamSettings == "" {
		return streamSettings, false, nil
	}

	settings := map[string]interface{}{}
	if err := json.Unmarshal([]byte(streamSettings), &settings); err != nil {
		return streamSettings, false, err
	}

	kcpSettings, ok := settings["kcpSettings"].(map[string]interface{})
	if !ok {
		return streamSettings, false, nil
	}

	changed := false
	if _, ok := kcpSettings["header"]; ok {
		delete(kcpSettings, "header")
		changed = true
	}
	if _, ok := kcpSettings["seed"]; ok {
		delete(kcpSettings, "seed")
		changed = true
	}
	if !changed {
		return streamSettings, false, nil
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return streamSettings, false, err
	}
	return string(data), true, nil
}

func RejectDeprecatedKcpHeaderSeed(streamSettings string) error {
	_, changed, err := StripDeprecatedKcpHeaderSeed(streamSettings)
	if err != nil {
		return err
	}
	if changed {
		return ErrDeprecatedKcpHeaderSeed
	}
	return nil
}
