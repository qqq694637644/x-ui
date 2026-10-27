package service

import "testing"

func TestNativeHysteriaBrutalRateBounds(t *testing.T) {
	for _, rate := range []string{"1mbps", "16mbps", "24mbps", "1.5mbps", "99999.99mbps"} {
		if err := validateHysteriaBrutalRate(rate); err != nil {
			t.Errorf("valid native Brutal rate %q rejected: %v", rate, err)
		}
	}
	for _, rate := range []string{"", "0mbps", "-1mbps", "0.5mbps", "16kbps", "NaNmbps", "100000mbps", "9999999999mbps", "16.123mbps", "16mbps\n20mbps"} {
		if err := validateHysteriaBrutalRate(rate); err == nil {
			t.Errorf("unsafe/invalid native Brutal rate %q accepted", rate)
		}
	}
}
