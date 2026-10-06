package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Load reads the TOML file at path over Defaults, rejects unknown keys, and runs
// Validate. Every problem is reported, each naming its key. A key of a section
// removed from an earlier release, such as the [[sources]] tables of v0.2, is
// an unknown key like any other: removed settings are never ignored.
func Load(path string) (Config, error) {
	f := fileConfig{Config: Defaults()}
	md, err := toml.DecodeFile(path, &f)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	cfg := f.Config
	// [sources] is a table; v0.2's [[sources]] array is left undecoded, so its
	// keys are reported below with the other unknown keys.
	if md.IsDefined("sources") && md.Type("sources") != "ArrayHash" {
		if err := md.PrimitiveDecode(f.Sources, &cfg.Sources); err != nil {
			return Config{}, fmt.Errorf("config %s: sources: %w", path, err)
		}
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return Config{}, fmt.Errorf("config %s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if err := Validate(&cfg); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// fileConfig is the decoding shape of a configuration file: Config, with the
// sources section held back as a primitive until its shape is known. The field
// shadows the embedded one of the same name.
type fileConfig struct {
	Config
	Sources toml.Primitive `toml:"sources"`
}

// Problems aggregates validation failures; each entry names its key.
type Problems []string

func (p Problems) Error() string {
	return "invalid configuration:\n  - " + strings.Join(p, "\n  - ")
}

// Err returns nil when there are no problems.
func (p Problems) Err() error {
	if len(p) == 0 {
		return nil
	}
	return p
}

// AsProblems extracts Problems from an error returned by Load or Validate.
func AsProblems(err error) (Problems, bool) {
	var p Problems
	ok := errors.As(err, &p)
	return p, ok
}
