package server

import (
	"os"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/pkg/errors"
	yml "gopkg.in/yaml.v3"
)

type AgentNode struct {
	Name  string
	Addr  string
	Token string
	// Labels are the labels the agent registered with, kept so the scheduler
	// has them after a restart without asking the agent.
	Labels map[string]string `yaml:"labels,omitempty"`
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

		// a fresh koanf per load: koanf merges successive loads, so a shared
		// instance would keep keys that were deleted from the file
		k := koanf.New(".")

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

// writeNodesToFile writes the config with nodes in place of c.Nodes and only
// sets c.Nodes once the write succeeded, so a failed write leaves c unchanged.
func (c *Config) writeNodesToFile(nodes []*AgentNode) error {
	next := *c
	next.Nodes = nodes

	if err := next.WriteToFile(); err != nil {
		return err
	}

	c.Nodes = nodes

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
