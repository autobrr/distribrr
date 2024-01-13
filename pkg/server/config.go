package server

import (
	"os"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

var k = koanf.New(".")

type Worker struct {
	Name               string
	Addr               string
	Token              string
	User               string
	Pass               string
	BasicUser          string
	BasicPass          string
	MaxActiveDownloads int
	//client             *qbittorrent.Client
}

type Config struct {
	Http Http `yaml:"http"`
	//Workers []*node.Node `yaml:"workers"`
	//Workers []*Worker `yaml:"workers"`
}

type Http struct {
	Host  string `yaml:"addr"`
	Port  string `yaml:"port"`
	Token string `yaml:"token"`
}

func NewConfig() *Config {
	c := &Config{}
	c.Defaults()

	return c
}

func (c *Config) Defaults() {
	c.Http = Http{
		Host:  "",
		Port:  "7422",
		Token: "",
	}
}

func (c *Config) LoadFromFile(configPath string) error {
	if configPath != "" {
		// create config if it doesn't exist
		if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
			//if writeErr := cfg.writeFile(configPath); writeErr != nil {
			//	log.Fatal().
			//		Err(writeErr).
			//		Str("service", "config").
			//		Msgf("failed writing %q", configPath)
			//}
		}

		if err := k.Load(file.Provider(configPath), yaml.Parser()); err != nil {
			log.Fatal().Err(err).Str("service", "config").Msgf("failed parsing %q", configPath)
		}

		// unmarshal
		if err := k.Unmarshal("", &c); err != nil {
			log.Fatal().Err(err).Str("service", "config").Msgf("failed unmarshalling %q", configPath)
		}
	}
	return nil
}
