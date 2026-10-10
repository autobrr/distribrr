package server

import (
	"os"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/pkg/errors"
	yml "gopkg.in/yaml.v3"
)

var k = koanf.New(".")

type AgentNode struct {
	Name  string
	Addr  string
	Token string
}

type Config struct {
	Http  Http         `yaml:"http"`
	Nodes []*AgentNode `yaml:"nodes"`

	configFile string `yaml:"-"`
}

type Http struct {
	Host  string `yaml:"host"`
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
	c.Nodes = make([]*AgentNode, 0)
}

func (c *Config) LoadFromFile(configPath string) error {
	if configPath != "" {
		c.configFile = configPath

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
			return errors.Wrapf(err, "could not parse config file: %s", configPath)
		}

		// unmarshal
		if err := k.Unmarshal("", &c); err != nil {
			return errors.Wrapf(err, "could not unmarshal config file: %s", configPath)
		}
	}
	return nil
}

func (c *Config) WriteToFile() error {
	if c.configFile != "" {
		// write file
		data, err := yml.Marshal(&c)
		if err != nil {
			return errors.Wrapf(err, "could not marshal config: %s", c.configFile)
		}

		if err := os.WriteFile(c.configFile, data, 0664); err != nil {
			return errors.Wrapf(err, "could not write config to file: %s", c.configFile)
		}
	}
	return nil
}
