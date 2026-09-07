package smtp

import (
	"strings"
	"testing"
)

func setRelayEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range []string{EnvAddr, EnvFrom, EnvUsername, EnvPassword} {
		t.Setenv(name, values[name])
	}
}

func TestLoadConfigFromEnv_ReadsTheRelay(t *testing.T) {
	setRelayEnv(t, map[string]string{
		EnvAddr: "mail:1025",
		EnvFrom: "notifier@fiapx.test",
	})

	config, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if config.Addr != "mail:1025" || config.From != "notifier@fiapx.test" {
		t.Fatalf("loaded %+v", config)
	}
	if config.Authenticates() {
		t.Error("no credentials were configured, so the relay must not be authenticated to")
	}
	if config.Host() != "mail" {
		t.Errorf("Host() = %q, want %q", config.Host(), "mail")
	}
}

func TestLoadConfigFromEnv_ReadsCredentials(t *testing.T) {
	setRelayEnv(t, map[string]string{
		EnvAddr:     "mail:587",
		EnvFrom:     "notifier@fiapx.test",
		EnvUsername: "relay-user",
		EnvPassword: "relay-password",
	})

	config, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("LoadConfigFromEnv() error = %v", err)
	}
	if !config.Authenticates() {
		t.Fatal("credentials were configured but Authenticates() is false")
	}
}

// Every refusal names the variable, because a notifier that cannot send is
// a notifier whose email preferences are stored and silently never honoured
// — the outcome the closed channel set exists to prevent — so its startup
// failure has to say which variable is missing rather than fail later
// against something it was never configured to reach.
func TestLoadConfigFromEnv_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantVar string
	}{
		{
			name:    "no relay address",
			env:     map[string]string{EnvFrom: "notifier@fiapx.test"},
			wantVar: EnvAddr,
		},
		{
			name:    "relay address is not host:port",
			env:     map[string]string{EnvAddr: "mail", EnvFrom: "notifier@fiapx.test"},
			wantVar: EnvAddr,
		},
		{
			name:    "no sender",
			env:     map[string]string{EnvAddr: "mail:1025"},
			wantVar: EnvFrom,
		},
		{
			name:    "the sender is not an address",
			env:     map[string]string{EnvAddr: "mail:1025", EnvFrom: "https://fiapx.test/notify"},
			wantVar: EnvFrom,
		},
		{
			// The sender lands in a message header exactly as the recipient
			// does, so it is judged by the same rule — a line break here
			// would be a header of the operator's choosing rather than a
			// registrant's, which is no better.
			name:    "the sender carries a line break",
			env:     map[string]string{EnvAddr: "mail:1025", EnvFrom: "notifier@fiapx.test\r\nBcc: elsewhere@example.test"},
			wantVar: EnvFrom,
		},
		{
			name:    "a username with no password",
			env:     map[string]string{EnvAddr: "mail:587", EnvFrom: "notifier@fiapx.test", EnvUsername: "relay-user"},
			wantVar: EnvUsername,
		},
		{
			name:    "a password with no username",
			env:     map[string]string{EnvAddr: "mail:587", EnvFrom: "notifier@fiapx.test", EnvPassword: "relay-password"},
			wantVar: EnvUsername,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRelayEnv(t, tt.env)

			config, err := LoadConfigFromEnv()
			if err == nil {
				t.Fatalf("LoadConfigFromEnv() accepted %+v", config)
			}
			if !strings.Contains(err.Error(), tt.wantVar) {
				t.Fatalf("error = %v, want it to name %s", err, tt.wantVar)
			}
			if config.Addr != "" || config.From != "" {
				t.Error("a refused configuration must be returned zeroed")
			}
		})
	}
}

// The password is a credential of this deployment's, and a refusal message
// is written to a log at startup.
func TestLoadConfigFromEnv_ARefusalDoesNotEchoTheCredential(t *testing.T) {
	setRelayEnv(t, map[string]string{
		EnvAddr:     "mail:587",
		EnvFrom:     "notifier@fiapx.test",
		EnvPassword: "a-relay-password-nobody-should-log",
	})

	_, err := LoadConfigFromEnv()
	if err == nil {
		t.Fatal("LoadConfigFromEnv() accepted a password with no username")
	}
	if strings.Contains(err.Error(), "a-relay-password-nobody-should-log") {
		t.Fatalf("the refusal echoes the credential: %v", err)
	}
}
