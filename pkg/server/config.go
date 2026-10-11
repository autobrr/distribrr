package server

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/autobrr/distribrr/pkg/token"

	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
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

// Validate checks the settings the server cannot run without.
func (c *Config) Validate() error {
	if err := token.Check(c.Http.Token); err != nil {
		return errors.Wrap(err, "http.token")
	}

	return nil
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

		if err := writeFileAtomic(c.configFile, data); err != nil {
			return errors.Wrapf(err, "could not write config to file: %s", c.configFile)
		}
	}
	return nil
}

// writeFileAtomic replaces path with data through a temp file and a rename, so
// a crash or a full disk mid-write leaves the old file instead of a truncated
// one. The file keeps its mode, 0664 for a new file, and a symlink is followed
// so the link stays. When the rename cannot work, because the file is a bind
// mount or its dir is not writable, the file is written in place.
func writeFileAtomic(path string, data []byte) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}

	perm := os.FileMode(0664)
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			log.Warn().Err(err).Msgf("could not create temp file next to %s, writing it in place", path)
			return os.WriteFile(path, data, perm)
		}

		return errors.Wrap(err, "could not create temp file")
	}

	// a no-op once the rename succeeded
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return errors.Wrap(err, "could not write temp file")
	}

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return errors.Wrap(err, "could not set temp file mode")
	}

	// flush before the rename, or a crash could leave an empty file in place
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.Wrap(err, "could not sync temp file")
	}

	if err := tmp.Close(); err != nil {
		return errors.Wrap(err, "could not close temp file")
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			log.Warn().Msgf("%s is a mount point and cannot be replaced, writing it in place", path)
			return os.WriteFile(path, data, perm)
		}

		return errors.Wrap(err, "could not replace config file")
	}

	return nil
}
