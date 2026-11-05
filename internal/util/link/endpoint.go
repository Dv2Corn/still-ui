package link

// Endpoint returns the server address and port an outbound dials, in any of the shapes Xray
// accepts: flat settings (VLESS, Hysteria), settings.vnext (VMess) or settings.servers (Trojan, SS).
func Endpoint(ob map[string]any) (string, int, bool) {
	settings, _ := ob["settings"].(map[string]any)
	if settings == nil {
		return "", 0, false
	}
	server := settings
	if _, flat := settings["address"]; !flat {
		for _, key := range []string{"vnext", "servers"} {
			if list, ok := settings[key].([]any); ok && len(list) > 0 {
				server, _ = list[0].(map[string]any)
				break
			}
		}
	}
	address, _ := server["address"].(string)
	port := num(server["port"])
	if address == "" || port <= 0 || port > 65535 {
		return "", 0, false
	}
	return address, port, true
}
