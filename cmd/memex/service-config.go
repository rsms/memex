package main

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v2"
)

type ServiceConfig struct {
	Start                interface{}   `yaml:",flow"`                  // string or []interface{}
	RestartLimitInterval time.Duration `yaml:"restart-limit-interval"` // e.g. 10s, 4.5s, 123ms
	Chroot               string
	Disabled             bool
	Env                  map[string]interface{}

	_start    []string          // parsed Start
	_env      map[string]string // preprocessed Env with expanded $(NAME)s
	_filename string            // non-empty when loaded from file
}

func ServiceConfigLoad(filename string) (*ServiceConfig, error) {
	s := &ServiceConfig{}
	return s, s.LoadFile(filename)
}

func (c *ServiceConfig) GetStartArgs() []string    { return c._start }
func (c *ServiceConfig) GetSourceFilename() string { return c._filename }
func (c *ServiceConfig) GetEnvEncodedList() []string {
	if len(c._env) == 0 {
		return nil
	}
	pairs := make([]string, len(c.Env))
	i := 0
	for k, v := range c._env {
		pairs[i] = k + "=" + v
		i++
	}
	return pairs
}

// IsEqual compares the recevier with other and returns true if they are equivalent
func (c *ServiceConfig) IsEqual(other *ServiceConfig) bool {
	if c.RestartLimitInterval != other.RestartLimitInterval ||
		c.Chroot != other.Chroot ||
		c.Disabled != other.Disabled ||
		len(c.GetStartArgs()) != len(other.GetStartArgs()) ||
		len(c.Env) != len(other.Env) {
		return false
	}
	for i := 0; i < len(c.GetStartArgs()); i++ {
		if c.GetStartArgs()[i] != other.GetStartArgs()[i] {
			return false
		}
	}
	for k, v := range c.Env {
		if other.Env[k] != v {
			return false
		}
	}
	for k, v := range other.Env {
		if c.Env[k] != v {
			return false
		}
	}
	return true
}

func (c *ServiceConfig) LoadFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.Load(f, filename)
}

func (c *ServiceConfig) YAMLString() string {
	data, _ := yaml.Marshal(c)
	return string(data)
}

func (c *ServiceConfig) Load(r io.Reader, filename string) error {
	c._filename = filename

	c.RestartLimitInterval = -1
	err := yaml.NewDecoder(r).Decode(c)
	if err != nil {
		return err
	}

	switch cmd := c.Start.(type) {

	case string:
		c._start = OSShellArgs(cmd)

	case []interface{}:
		if len(cmd) == 0 {
			return errorf("start is an empty list; missing program")
		}
		args := make([]string, len(cmd))
		for i, any := range cmd {
			switch v := any.(type) {
			case string:
				args[i] = v
			default:
				args[i] = fmt.Sprintf("%v", any)
			}
		}
		c._start = args

	default:
		return errorf("start must be string or lsit of strings (got %T)", cmd)

	} // switch

	// preprocess env
	c._env = make(map[string]string, len(c.Env))
	for k, v := range c.Env {
		c._env[k] = fmt.Sprint(v)
	}

	// replace $(NAME) or $(NAME:default) with env vars
	re := regexp.MustCompile(`\$\([A-Za-z0-9_][^):]*(?::[^)]+|)\)`)
	var envNotFound []string
	subEnvVars := func(str string) string {
		return re.ReplaceAllStringFunc(str, func(s string) string {
			name := s[2 : len(s)-1]
			// see if there'a a default value
			defaultValue := ""
			defaultIndex := strings.IndexByte(name, ':')
			if defaultIndex >= 0 {
				defaultValue = name[defaultIndex+1:]
				name = name[:defaultIndex]
			}
			name = strings.TrimSpace(name)
			value, found := c._env[name]
			if found {
				return value
			}
			value, found = syscall.Getenv(name)
			if !found {
				if defaultIndex >= 0 {
					value = defaultValue
				} else {
					envNotFound = append(envNotFound, name)
				}
			}
			return value
		})
	}

	// subEnvVars of env
	for k, v := range c._env {
		c._env[k] = subEnvVars(v)
	}

	// subEnvVars of args
	for i := 0; i < len(c._start); i++ {
		c._start[i] = subEnvVars(c._start[i])
	}

	if len(envNotFound) > 0 {
		if len(envNotFound) == 1 {
			err = errorf("variable not found in environment: %q", envNotFound[0])
		} else {
			err = errorf("variables not found in environment: %q", envNotFound)
		}
	}

	return err
}
