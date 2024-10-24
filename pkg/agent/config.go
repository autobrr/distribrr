package agent

import (
	"os"

	"github.com/autobrr/go-qbittorrent"
	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

var k = koanf.New(".")

type Config struct {
	Http    Http                   `yaml:"http"`
	Agent   Agent                  `yaml:"agent"`
	Manager Manager                `yaml:"manager"`
	Clients map[string]*QbitClient `yaml:"clients"`
}

type Agent struct {
	NodeName   string            `yaml:"nodeName"`
	ClientAddr string            `yaml:"clientAddr"`
	Labels     map[string]string `yaml:"labels"`
}

type Manager struct {
	Addr  string `yaml:"addr"`
	Token string `yaml:"token"`
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
		Port:  "7430",
		Token: "",
	}
	c.Manager = Manager{}
	c.Agent = Agent{
		NodeName:   "",
		ClientAddr: "",
		Labels:     map[string]string{},
	}
	c.Clients = map[string]*QbitClient{}
	//c.Clients = make(map[string]*QbitClient, 0)
}

func (c *Config) LoadFromFile(configPath string) error {
	if configPath != "" {
		if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
			log.Fatal().Err(err).Str("service", "config").Msgf("config file does not exist: %q", configPath)
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

type QbitClient struct {
	Name      string      `yaml:"name"`
	Host      string      `yaml:"host"`
	User      string      `yaml:"user"`
	Pass      string      `yaml:"pass"`
	BasicUser string      `yaml:"basicUser"`
	BasicPass string      `yaml:"basicPass"`
	Paths     []string    `yaml:"paths"`
	Rules     ClientRules `yaml:"rules"`
	Client    *qbittorrent.Client
}

type ClientRules struct {
	//MaxActiveDownloads int           `yaml:"maxActiveDownloads"`
	//FreeSpace          []string      `yaml:"freeSpace"`
	Torrents TorrentRules  `yaml:"torrents"`
	Storage  []StorageRule `yaml:"storage"`
}

type StorageRule struct {
	Path     string `yaml:"path"`
	Tier     int    `yaml:"tier"`
	MinFree  string `yaml:"minFree"`
	MaxUsage string `yaml:"maxUsage"`
}

type TorrentRules struct {
	MaxActiveDownloads int `yaml:"maxActiveDownloads"`
}
