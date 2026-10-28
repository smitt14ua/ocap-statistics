package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestLoginFromEnv(t *testing.T) {
	t.Setenv("OCAPSTATS_OCAP_URL", "https://ocap.example.com")
	t.Setenv("OCAPSTATS_LOGIN", "OIDC")
	t.Setenv("OIDC_ISSUER", "https://id.example.com/realms/test")
	t.Setenv("OIDC_CLIENT_ID", "stats")
	t.Setenv("OIDC_CLIENT_SECRET", "s3cret")
	t.Setenv("OIDC_SCOPES", "profile  steam")
	t.Setenv("OIDC_PROVIDER_NAME", "Example ID")
	t.Setenv("OIDC_STEAM_ID_CLAIM", "steamid64")
	t.Setenv("OIDC_PICTURE_CLAIM", "avatar")
	t.Setenv("OIDC_NAME_CLAIM", "nick")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := OIDC{
		Issuer: "https://id.example.com/realms/test", ClientID: "stats", ClientSecret: "s3cret",
		Scopes: []string{"openid", "profile", "steam"}, ProviderName: "Example ID",
		SteamIDClaim: "steamid64", PictureClaim: "avatar", NameClaim: "nick",
	}
	got := cfg.OIDC
	if cfg.Login != LoginOIDC || !slices.Equal(got.Scopes, want.Scopes) {
		t.Fatalf("login = %q, scopes = %q", cfg.Login, got.Scopes)
	}
	got.Scopes, want.Scopes = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("oidc = %+v, want %+v", got, want)
	}
}

func TestLoginDefaults(t *testing.T) {
	t.Setenv("OCAPSTATS_OCAP_URL", "https://ocap.example.com")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.OIDC
	if cfg.Login != "" || strings.Join(o.Scopes, " ") != "openid profile" || o.ProviderName != "SSO" ||
		o.SteamIDClaim != "steam_id" || o.PictureClaim != "picture" || o.NameClaim != "name" {
		t.Errorf("defaults: login %q, oidc %+v", cfg.Login, o)
	}

	// The older switch still turns the Steam sign-in on, unless a method is chosen.
	t.Setenv("OCAPSTATS_STEAM_LOGIN", "true")
	if cfg, err := Load(""); err != nil || cfg.Login != LoginSteam {
		t.Errorf("OCAPSTATS_STEAM_LOGIN=true: login %q, %v", cfg.Login, err)
	}
	t.Setenv("OCAPSTATS_LOGIN", "off")
	if cfg, err := Load(""); err != nil || cfg.Login != "" {
		t.Errorf("OCAPSTATS_LOGIN=off: login %q, %v", cfg.Login, err)
	}
}

func TestLoginInvalid(t *testing.T) {
	t.Setenv("OCAPSTATS_OCAP_URL", "https://ocap.example.com")
	for name, env := range map[string]map[string]string{
		"unknown method":   {"OCAPSTATS_LOGIN": "google"},
		"no client id":     {"OCAPSTATS_LOGIN": "oidc", "OIDC_ISSUER": "https://id.example.com"},
		"no issuer":        {"OCAPSTATS_LOGIN": "oidc", "OIDC_CLIENT_ID": "stats"},
		"issuer not a URL": {"OCAPSTATS_LOGIN": "oidc", "OIDC_CLIENT_ID": "stats", "OIDC_ISSUER": "id.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(""); err == nil {
				t.Error("accepted")
			}
		})
	}
}
