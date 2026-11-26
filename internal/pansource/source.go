// Package pansource resolves which online-library backend miyabi reads through
// and persists the operator's choice in the settings table. A stored row wins
// over the environment, so deployments configured only through environment
// variables keep behaving exactly as before.
package pansource

import (
	"context"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/ppxb/miyabi/internal/database"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/drive/cd2"
	"github.com/ppxb/miyabi/internal/ent"
)

const (
	// Provider115 reads the online library straight through the 115 API.
	Provider115 = "115"
	// ProviderCD2 reads it through a CloudDrive2 instance.
	ProviderCD2 = "cd2"

	providerSetting = "pan.provider"
	cd2Setting      = "pan.cd2"
)

// CD2 holds the CloudDrive2 connection settings. The type doubles as the
// persisted JSON row and the request body of the settings endpoints; empty
// fields in a request mean "keep the stored value".
type CD2 struct {
	Endpoint string `json:"endpoint,omitempty"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Account  string `json:"account,omitempty"`
}

// Settings is the resolved configuration: database over environment.
type Settings struct {
	Provider string
	CD2      CD2
}

// Input is the body of a pan-source update.
type Input struct {
	Provider string `json:"provider"`
	CD2      CD2    `json:"cd2"`
}

// Status is the API projection of the settings. Secrets are reduced to the
// fact that they are set; their values never leave the process.
type Status struct {
	Provider          string  `json:"provider"`
	EffectiveProvider string  `json:"effectiveProvider"`
	RequiresRestart   bool    `json:"requiresRestart"`
	CD2               CD2View `json:"cd2"`
}

// CD2View is the redacted form of CD2.
type CD2View struct {
	Endpoint    string `json:"endpoint"`
	Account     string `json:"account"`
	Username    string `json:"username"`
	HasToken    bool   `json:"hasToken"`
	HasPassword bool   `json:"hasPassword"`
}

// Service reads and writes the pan-source settings. startup is the provider
// the running process assembled its drive client with; it only changes on
// restart, which is what RequiresRestart reports.
type Service struct {
	database *ent.Client
	env      Settings
	startup  Settings
}

// New resolves the effective settings and records them as the running ones.
// An unreadable row falls back to the environment so a damaged database does
// not prevent startup.
func New(ctx context.Context, database *ent.Client, logger *slog.Logger, env Settings) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	env.Provider = normalizeProvider(env.Provider)
	service := &Service{database: database, env: env, startup: env}
	resolved, err := service.resolve(ctx)
	if err != nil {
		logger.WarnContext(ctx, "pan source settings unreadable; using environment", "error", err)
		return service
	}
	service.startup = resolved
	return service
}

// Startup returns the settings the running process was assembled with.
func (service *Service) Startup() Settings { return service.startup }

// CD2Config maps the resolved settings onto the adapter constructor argument.
func (settings Settings) CD2Config() cd2.Config {
	return cd2.Config{
		Endpoint: settings.CD2.Endpoint,
		Token:    settings.CD2.Token,
		Username: settings.CD2.Username,
		Password: settings.CD2.Password,
		Account:  settings.CD2.Account,
	}
}

// Status reports the saved settings alongside the running provider.
func (service *Service) Status(ctx context.Context) (Status, error) {
	settings, err := service.resolve(ctx)
	if err != nil {
		return Status{}, err
	}
	return service.status(settings), nil
}

// Update validates and persists the settings. Empty CD2 fields keep their
// stored value, so a page that never received a secret can save safely.
func (service *Service) Update(ctx context.Context, input Input) (Status, error) {
	provider := normalizeProvider(input.Provider)
	if err := validateProvider(provider); err != nil {
		return Status{}, err
	}
	stored, _, err := database.LoadSetting[CD2](ctx, service.database, cd2Setting)
	if err != nil {
		return Status{}, err
	}
	merged := stored.overlay(input.CD2)
	// Only the CloudDrive2 backend has connection requirements; picking 115 must
	// stay possible on a deployment that has no CloudDrive2 settings at all.
	if provider == ProviderCD2 {
		if err := validateCD2(merged.withFallback(service.env.CD2)); err != nil {
			return Status{}, err
		}
	}
	if err := database.SaveSetting(ctx, service.database, providerSetting, provider); err != nil {
		return Status{}, err
	}
	if merged != stored {
		if err := database.SaveSetting(ctx, service.database, cd2Setting, merged); err != nil {
			return Status{}, err
		}
	}
	return service.Status(ctx)
}

func (service *Service) resolve(ctx context.Context) (Settings, error) {
	provider, _, err := database.LoadSetting[string](ctx, service.database, providerSetting)
	if err != nil {
		return Settings{}, err
	}
	if strings.TrimSpace(provider) == "" {
		provider = service.env.Provider
	}
	stored, _, err := database.LoadSetting[CD2](ctx, service.database, cd2Setting)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Provider: normalizeProvider(provider), CD2: stored.withFallback(service.env.CD2)}, nil
}

func (service *Service) status(settings Settings) Status {
	return Status{
		Provider:          settings.Provider,
		EffectiveProvider: service.startup.Provider,
		RequiresRestart:   service.restartRequired(settings),
		CD2: CD2View{
			Endpoint:    settings.CD2.Endpoint,
			Account:     settings.CD2.Account,
			Username:    settings.CD2.Username,
			HasToken:    settings.CD2.Token != "",
			HasPassword: settings.CD2.Password != "",
		},
	}
}

// restartRequired reports whether the running backend no longer matches the
// saved settings. Switching providers always needs a restart; CloudDrive2
// connection details only matter while CloudDrive2 is the running backend.
func (service *Service) restartRequired(settings Settings) bool {
	if settings.Provider != service.startup.Provider {
		return true
	}
	return settings.Provider == ProviderCD2 && settings.CD2 != service.startup.CD2
}

// overlay applies the fields the operator filled in and keeps the stored ones
// for empty fields. The password keeps its exact value, whitespace included.
func (cd2 CD2) overlay(input CD2) CD2 {
	keep := func(value, stored string) string {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
		return stored
	}
	cd2.Endpoint = keep(input.Endpoint, cd2.Endpoint)
	cd2.Token = keep(input.Token, cd2.Token)
	cd2.Username = keep(input.Username, cd2.Username)
	cd2.Account = keep(input.Account, cd2.Account)
	if strings.TrimSpace(input.Password) != "" {
		cd2.Password = input.Password
	}
	return cd2
}

// withFallback fills empty fields from the environment.
func (cd2 CD2) withFallback(fallback CD2) CD2 {
	fill := func(value, fallback string) string {
		if value != "" {
			return value
		}
		return fallback
	}
	cd2.Endpoint = fill(cd2.Endpoint, fallback.Endpoint)
	cd2.Token = fill(cd2.Token, fallback.Token)
	cd2.Username = fill(cd2.Username, fallback.Username)
	cd2.Password = fill(cd2.Password, fallback.Password)
	cd2.Account = fill(cd2.Account, fallback.Account)
	return cd2
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return Provider115
	}
	return provider
}

func validateProvider(provider string) error {
	if provider != Provider115 && provider != ProviderCD2 {
		return domain.E(domain.KindInvalid, "数据源只能是 115 或 cd2", nil)
	}
	return nil
}

// validateCD2 checks the settings a restart would assemble the adapter with.
func validateCD2(cd2 CD2) error {
	if cd2.Endpoint == "" {
		return domain.E(domain.KindInvalid, "使用 CloudDrive2 时必须填写接入地址", nil)
	}
	if !validEndpoint(cd2.Endpoint) {
		return domain.E(domain.KindInvalid, "CloudDrive2 接入地址不是合法的 URL", nil)
	}
	if cd2.Token == "" && (cd2.Username == "" || cd2.Password == "") {
		return domain.E(domain.KindInvalid, "使用 CloudDrive2 时必须填写 API Token 或用户名 + 密码", nil)
	}
	return nil
}

// validEndpoint accepts what the adapter itself accepts: an http(s) URL or a
// bare host:port that defaults to plaintext gRPC.
func validEndpoint(endpoint string) bool {
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Host != "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") {
		return true
	}
	host, port, err := net.SplitHostPort(endpoint)
	return err == nil && host != "" && port != ""
}
