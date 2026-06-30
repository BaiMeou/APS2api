package config

type ConfigProvider interface {
	GetPortAPI() int
	GetMaxRetries() int
	GetAdminPassword() string
	GetProxyURL() string
	GetDebugPprof() bool
	GetDebugMode() bool

	GetAnti429Enabled() bool
	GetAnti429Target() string
	GetAntiTracking() bool
	GetDropMaxTokens() bool

	GetForceNoStream() bool
	GetMaxN() int
	GetMaxRequestMB() int
	GetMaxSpillMB() int

	GetTokenPoolSize() int
	GetRecaptchaExpireSeconds() int
	GetVertexAPIKey() string
	GetCountTokensQuerySignature() string

	GetSafetySettings() map[string]string

	GetParallelPoolEnabled() bool
	GetStickyPoolEnabled() bool
	GetParallelPoolSize() int
	GetParallelPoolMaxRounds() int
	GetParallelPoolDelayDynamic() bool
	GetParallelPoolDelayMs() int
	GetActiveNodeURI() string
	GetParallelNodeTopK() int

	GetBackgroundImage() string
	GetFontSize() string
	GetFontColorType() string
	GetFontColor() string
	GetCustomBgPresets() []string

	GetTelemetryEnabled() *bool

	BaseModels() []string
	AliasMap() map[string]string
	ModelsWithFakeVariants() []string
	FakePrefixes() []string
	ResolveModelName(string) string

	ConfigDir() string
	ConfigPath() string
}

type ConfigWriter interface {
	WriteSettings(map[string]any) error
	WriteModels([]string, map[string]string) error
}
