// Package provider is a plugin to use a proxmox cluster as an provider.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/NX211/traefik-proxmox-provider/dynamic"
	"github.com/NX211/traefik-proxmox-provider/internal"
)

// Config the plugin configuration.
type Config struct {
	PollInterval   string `json:"pollInterval" yaml:"pollInterval" toml:"pollInterval"`
	ApiEndpoint    string `json:"apiEndpoint" yaml:"apiEndpoint" toml:"apiEndpoint"`
	ApiTokenId     string `json:"apiTokenId" yaml:"apiTokenId" toml:"apiTokenId"`
	ApiToken       string `json:"apiToken" yaml:"apiToken" toml:"apiToken"`
	ApiLogging     string `json:"apiLogging" yaml:"apiLogging" toml:"apiLogging"`
	ApiValidateSSL string `json:"apiValidateSSL" yaml:"apiValidateSSL" toml:"apiValidateSSL"`
}

// CreateConfig creates the default plugin configuration.
func CreateConfig() *Config {
	return &Config{
		PollInterval:   "30s", // Default to 30 seconds for polling
		ApiValidateSSL: "true",
		ApiLogging:     "info",
	}
}

// Provider a plugin.
type Provider struct {
	name         string
	pollInterval time.Duration
	client       *internal.ProxmoxClient
	cancel       func()
}

// New creates a new Provider plugin.
func New(ctx context.Context, config *Config, name string) (*Provider, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	pi, err := time.ParseDuration(config.PollInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid poll interval: %w", err)
	}

	// Ensure minimum poll interval
	if pi < 5*time.Second {
		return nil, fmt.Errorf("poll interval must be at least 5 seconds, got %v", pi)
	}

	pc, err := newParserConfig(
		config.ApiEndpoint,
		config.ApiTokenId,
		config.ApiToken,
	)
	if err != nil {
		return nil, fmt.Errorf("invalid parser config: %w", err)
	}

	pc.LogLevel = config.ApiLogging
	pc.ValidateSSL = config.ApiValidateSSL == "true"
	client := newClient(pc)

	if err := logVersion(client, ctx); err != nil {
		return nil, fmt.Errorf("failed to get Proxmox version: %w", err)
	}

	return &Provider{
		name:         name,
		pollInterval: pi,
		client:       client,
	}, nil
}

// Init the provider.
func (p *Provider) Init() error {
	return nil
}

// Provide creates and send dynamic configuration.
func (p *Provider) Provide(cfgChan chan<- json.Marshaler) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("Recovered from panic in provider: %v", err)
			}
		}()

		p.loadConfiguration(ctx, cfgChan)
	}()

	return nil
}

func (p *Provider) loadConfiguration(ctx context.Context, cfgChan chan<- json.Marshaler) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	// Initial configuration
	if err := p.updateConfiguration(ctx, cfgChan); err != nil {
		log.Printf("Error during initial configuration: %v", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := p.updateConfiguration(ctx, cfgChan); err != nil {
				log.Printf("Error updating configuration: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *Provider) updateConfiguration(ctx context.Context, cfgChan chan<- json.Marshaler) error {
	servicesMap, err := getServiceMap(p.client, ctx)
	if err != nil {
		return fmt.Errorf("error getting service map: %w", err)
	}

	configuration := generateConfiguration(servicesMap)
	cfgChan <- &dynamic.JSONPayload{Configuration: configuration}
	return nil
}

// Stop to stop the provider and the related go routines.
func (p *Provider) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

// ParserConfig represents the configuration for the Proxmox API client
type ParserConfig struct {
	ApiEndpoint string
	TokenId     string
	Token       string
	LogLevel    string
	ValidateSSL bool
}

func newParserConfig(apiEndpoint, tokenID, token string) (ParserConfig, error) {
	if apiEndpoint == "" || tokenID == "" || token == "" {
		return ParserConfig{}, errors.New("missing mandatory values: apiEndpoint, tokenID or token")
	}
	return ParserConfig{
		ApiEndpoint: apiEndpoint,
		TokenId:     tokenID,
		Token:       token,
		LogLevel:    "info",
		ValidateSSL: true,
	}, nil
}

// validateConfig validates the plugin configuration
func validateConfig(config *Config) error {
	if config == nil {
		return errors.New("configuration cannot be nil")
	}

	if config.PollInterval == "" {
		return errors.New("poll interval must be set")
	}

	if config.ApiEndpoint == "" {
		return errors.New("API endpoint must be set")
	}

	if config.ApiTokenId == "" {
		return errors.New("API token ID must be set")
	}

	if config.ApiToken == "" {
		return errors.New("API token must be set")
	}

	return nil
}

func isBoolLabelEnabled(labels map[string]string, label string) bool {
	val, exists := labels[label]
	return exists && val == "true"
}
