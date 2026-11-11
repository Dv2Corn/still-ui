package panelimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// ReadXUI reads the inbounds of an x-ui database (vaxilu/x-ui and its forks, 3x-ui before
// v3), where clients live only inside each inbound's settings JSON.
func ReadXUI(dbPath string) (*Plan, error) {
	gdb, closeDB, err := openSource(dbPath)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if !hasTable(gdb, "inbounds") {
		return nil, errors.New("not an x-ui database: the inbounds table is missing")
	}
	if hasTable(gdb, "proxies") && hasTable(gdb, "jwt") {
		return nil, errors.New("this is a Marzban database: import it as marzban")
	}
	if hasTable(gdb, "clients") {
		return nil, errors.New("this is a 3x-ui v3 or still-ui database: restore it as a database backup or run the 3x-ui migration instead")
	}
	inboundRows, err := rows(gdb, "inbounds")
	if err != nil {
		return nil, err
	}
	trafficRows, err := rows(gdb, "client_traffics")
	if err != nil {
		return nil, err
	}
	traffic := map[string]map[string]any{}
	for _, t := range trafficRows {
		traffic[asString(t["email"])] = t
	}
	users, err := rows(gdb, "users")
	if err != nil {
		return nil, err
	}

	plan := &Plan{Source: SourceXUI, Hosts: map[string][]model.Host{}}
	for _, u := range users {
		plan.Admins = append(plan.Admins, asString(u["username"]))
	}
	seen := map[string]bool{}
	for _, r := range inboundRows {
		row := &model.Inbound{
			Up: asInt(r["up"]), Down: asInt(r["down"]), Total: asInt(r["total"]),
			Remark: asString(r["remark"]), Enable: asInt(r["enable"]) != 0, ExpiryTime: asInt(r["expiry_time"]),
			Listen: asString(r["listen"]), Port: int(asInt(r["port"])), Protocol: model.Protocol(asString(r["protocol"])),
			StreamSettings: asString(r["stream_settings"]), Tag: asString(r["tag"]), Sniffing: asString(r["sniffing"]),
		}
		name := row.Tag
		if name == "" {
			name = "inbound-" + strconv.Itoa(row.Port)
		}
		settings := map[string]any{}
		if err := json.Unmarshal([]byte(asString(r["settings"])), &settings); err != nil {
			plan.note("inbound %q: unreadable settings (%v), not imported", name, err)
			continue
		}
		clients, _ := settings["clients"].([]any)
		for i, raw := range clients {
			c, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			email := strings.TrimSpace(asString(c["email"]))
			if email == "" {
				// vaxilu/x-ui clients have no name; the panel here keys everything by it.
				email = fmt.Sprintf("%s-%d", name, i+1)
				plan.note("inbound %q: client %d has no email, named %q", name, i+1, email)
			}
			c["email"] = email
			// The same repairs "x-ui migrate" makes on an old database in place.
			if id, ok := c["tgId"].(string); ok {
				c["tgId"], _ = strconv.ParseInt(strings.ReplaceAll(id, " ", ""), 10, 64)
			}
			if c["flow"] == "xtls-rprx-direct" {
				c["flow"] = ""
			}
			if seen[strings.ToLower(email)] {
				continue
			}
			seen[strings.ToLower(email)] = true
			if t := traffic[email]; t != nil {
				row.ClientStats = append(row.ClientStats, xray.ClientTraffic{
					Email: email, Enable: asInt(t["enable"]) != 0, Up: asInt(t["up"]), Down: asInt(t["down"]),
					Total: asInt(t["total"]), ExpiryTime: asInt(t["expiry_time"]),
				})
			}
		}
		b, err := json.Marshal(settings)
		if err != nil {
			return nil, err
		}
		row.Settings = string(b)
		plan.Inbounds = append(plan.Inbounds, row)
	}
	return plan, nil
}
