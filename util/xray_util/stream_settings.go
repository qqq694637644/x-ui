package xray_util

import (
	"encoding/json"
	"fmt"
)

func ValidateXray26327StreamSettings(streamSettings string) error {
	if streamSettings == "" {
		return nil
	}

	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(streamSettings), &settings); err != nil {
		return err
	}

	if rawNetwork, ok := settings["network"]; ok {
		var network string
		if err := json.Unmarshal(rawNetwork, &network); err != nil {
			return err
		}
		if network == "kcp" {
			return fmt.Errorf("Xray-core 26.3.27 不支持旧 streamSettings.network=kcp，请改为 mkcp")
		}
	}

	rawKcpSettings, ok := settings["kcpSettings"]
	if !ok {
		return nil
	}
	kcpSettings := map[string]json.RawMessage{}
	if err := json.Unmarshal(rawKcpSettings, &kcpSettings); err != nil {
		return err
	}
	if _, ok := kcpSettings["header"]; ok {
		return fmt.Errorf("Xray-core 26.3.27 不支持 kcpSettings.header，请迁移到 FinalMask")
	}
	if _, ok := kcpSettings["seed"]; ok {
		return fmt.Errorf("Xray-core 26.3.27 不支持 kcpSettings.seed，请迁移到 FinalMask")
	}
	return nil
}
