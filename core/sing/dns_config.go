package sing

import (
	"encoding/json"
	"strings"
)

func normalizeLegacyDNSAddress(address string) string {
	for _, scheme := range []string{"tcp", "udp"} {
		prefix := scheme + ":"
		if strings.HasPrefix(address, prefix) && !strings.HasPrefix(address, scheme+"://") {
			return scheme + "://" + strings.TrimPrefix(address, prefix)
		}
	}
	return address
}

func withNormalizedSingOriginDNS(raw []byte) []byte {
	cfg := map[string]interface{}{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw
	}
	dns, ok := cfg["dns"].(map[string]interface{})
	if !ok {
		return raw
	}
	servers, ok := dns["servers"].([]interface{})
	if !ok {
		return raw
	}
	changed := false
	for _, item := range servers {
		server, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		address, ok := server["address"].(string)
		if !ok || address == "" {
			continue
		}
		normalized := normalizeLegacyDNSAddress(address)
		if normalized == address {
			continue
		}
		server["address"] = normalized
		changed = true
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return raw
	}
	return out
}
