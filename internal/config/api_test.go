package config

import (
	"testing"
)

func TestAPIConfigLoadsGatewayClients(t *testing.T) {
	c, err := yamlConfig(t, "config.yaml", `api:
  enabled: true
  listen: 127.0.0.1:8765
  gateway:
    enabled: true
    listen: 127.0.0.1:8766
    publicUrl: https://bp.example/mcp
    clients:
      phone:
        agents: [worker]
        readOnlyBoards: [main]
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.API == nil || !c.API.Enabled || c.API.Listen != "127.0.0.1:8765" || c.API.Gateway == nil {
		t.Fatalf("api = %#v", c.API)
	}
	phone := c.API.Gateway.Clients["phone"]
	if len(phone.Agents) != 1 || phone.Agents[0] != "worker" || phone.ReadOnlyBoards[0] != "main" {
		t.Fatalf("phone = %#v", phone)
	}
}

func TestAPIConfigRejectsNonLoopbackAndBadNames(t *testing.T) {
	for name, body := range map[string]string{
		"public listen":         "api:\n  listen: 0.0.0.0:8765\n",
		"hostname listen":       "api:\n  listen: localhost:8765\n",
		"public gateway":        "api:\n  gateway:\n    listen: 192.0.2.1:8766\n",
		"enabled without addr":  "api:\n  gateway:\n    enabled: true\n",
		"plain http public url": "api:\n  gateway:\n    listen: 127.0.0.1:1\n    publicUrl: http://bp.example/mcp\n",
		"bad client name":       "api:\n  gateway:\n    clients:\n      \"bad name\": {}\n",
		"bad exposed agent":     "api:\n  gateway:\n    clients:\n      phone:\n        agents: [\"../x\"]\n",
	} {
		if _, err := yamlConfig(t, "config.yaml", body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
