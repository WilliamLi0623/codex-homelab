package sessionruntime

import (
	"encoding/json"
	"testing"
)

func TestBootstrapNetworkEnableFormUsesDigestAndPreservesNICOptions(t *testing.T) {
	config := map[string]json.RawMessage{"digest": json.RawMessage(`"0123456789012345678901234567890123456789"`), "net0": json.RawMessage(`"name=eth0,bridge=vmbr0,hwaddr=AA:BB:CC:DD:EE:FF,ip=dhcp,link_down=1,tag=20,firewall=1"`), "net1": json.RawMessage(`"name=eth1,ip=dhcp,link_down=1"`)}
	form, err := bootstrapNetworkEnableForm(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(form) != 3 || form.Get("digest") != "0123456789012345678901234567890123456789" || form.Get("net0") != "name=eth0,bridge=vmbr0,hwaddr=AA:BB:CC:DD:EE:FF,ip=dhcp,link_down=0,tag=20,firewall=1" || form.Get("net1") != "name=eth1,ip=dhcp,link_down=0" {
		t.Fatalf("unexpected patch: %v", form)
	}
}

func TestBootstrapNetworkEnableFormRejectsUnverifiedSnapshots(t *testing.T) {
	for _, tc := range []struct{ name, digest, network string }{
		{"missing digest", `null`, `"name=eth0,ip=dhcp,link_down=1"`},
		{"invalid digest", `"not-a-digest"`, `"name=eth0,ip=dhcp,link_down=1"`},
		{"unfenced", `"0123456789012345678901234567890123456789"`, `"name=eth0,ip=dhcp,link_down=0"`},
		{"duplicate", `"0123456789012345678901234567890123456789"`, `"name=eth0,ip=dhcp,link_down=1,link_down=1"`},
		{"static", `"0123456789012345678901234567890123456789"`, `"name=eth0,ip=10.0.0.1/24,link_down=1"`},
		{"no NIC", `"0123456789012345678901234567890123456789"`, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]json.RawMessage{"digest": json.RawMessage(tc.digest)}
			if tc.network != "" {
				config["net0"] = json.RawMessage(tc.network)
			}
			if form, err := bootstrapNetworkEnableForm(config); err == nil || form != nil {
				t.Fatalf("unsafe patch=%v err=%v", form, err)
			}
		})
	}
}
