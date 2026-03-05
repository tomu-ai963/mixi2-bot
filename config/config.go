package config

import (
	"log"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	// Authentication
	ClientID     string `envconfig:"CLIENT_ID" required:"true"`
	ClientSecret string `envconfig:"CLIENT_SECRET" required:"true"`
	TokenURL     string `envconfig:"TOKEN_URL" required:"true"`

	// API endpoints
	APIAddress    string `envconfig:"API_ADDRESS"`
	StreamAddress string `envconfig:"STREAM_ADDRESS"`

	// Webhook settings
	SignaturePublicKey string `envconfig:"SIGNATURE_PUBLIC_KEY"`
	Port               string `envconfig:"PORT" default:"8080"`
}

// GetConfig loads configuration from environment variables.
func GetConfig() *Config {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	return &c
}
