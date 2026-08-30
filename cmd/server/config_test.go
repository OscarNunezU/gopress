package main

import "testing"

// TestValidateConfig_APIKeyRequiredOutsideDev pins the rule that outside
// development an empty GOPRESS_API_KEY leaves POST /pdf open, so startup must
// fail. Development may run keyless on purpose.
func TestValidateConfig_APIKeyRequiredOutsideDev(t *testing.T) {
	base := config{poolSize: 4, port: 3000, chromeBin: "/usr/bin/chrome"}

	cases := []struct {
		name    string
		env     string
		apiKey  string
		wantErr bool
	}{
		{"dev without key: allowed", "development", "", false},
		{"dev with key: allowed", "development", "a-thirty-two-character-long-key!", false},
		{"prod without key: REJECTED", "production", "", true},
		{"prod with key: allowed", "production", "a-thirty-two-character-long-key!", false},
		{"staging without key: REJECTED", "staging", "", true},
		{"prod with short key: REJECTED by length", "production", "short", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base
			cfg.env = c.env
			cfg.apiKey = c.apiKey
			err := validateConfig(cfg)
			if (err != nil) != c.wantErr {
				t.Fatalf("env=%q apiKey=%q: err=%v, wantErr=%v", c.env, c.apiKey, err, c.wantErr)
			}
		})
	}
}
