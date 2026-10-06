package config

import (
	"bufio"
	"encoding"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const (
	examplePath  = "../../deploy/examples/precious.toml"
	examplesGlob = "../../deploy/examples/*.toml"
	operatorDocs = "../../docs/operator.md"
)

// configKey is one leaf TOML key of Config, such as "server.listen", with its
// Go type and default value.
type configKey struct {
	name string
	typ  reflect.Type
	def  reflect.Value
}

// configKeys lists every leaf key of Config by walking its toml tags. Types
// that decode from text (Duration) are leaves.
func configKeys(t *testing.T) []configKey {
	t.Helper()
	textType := reflect.TypeFor[encoding.TextUnmarshaler]()
	var keys []configKey
	var walk func(typ reflect.Type, v reflect.Value, prefix string)
	walk = func(typ reflect.Type, v reflect.Value, prefix string) {
		for i := range typ.NumField() {
			f := typ.Field(i)
			tag := f.Tag.Get("toml")
			if tag == "" || tag == "-" {
				t.Fatalf("field %s%s has no toml tag", prefix, f.Name)
			}
			fv := v.Field(i)
			switch {
			case reflect.PointerTo(f.Type).Implements(textType):
				keys = append(keys, configKey{prefix + tag, f.Type, fv})
			case f.Type.Kind() == reflect.Struct:
				walk(f.Type, fv, prefix+tag+".")
			default:
				keys = append(keys, configKey{prefix + tag, f.Type, fv})
			}
		}
	}
	walk(reflect.TypeFor[Config](), reflect.ValueOf(Defaults()), "")
	return keys
}

// TestExampleConfig checks deploy/examples/precious.toml, the only example:
// it loads strictly, it mentions every key, and each commented
// "# key = value" line, when uncommented, is a valid key holding exactly the
// default value.
func TestExampleConfig(t *testing.T) {
	paths, err := filepath.Glob(examplesGlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "precious.toml" {
		t.Fatalf("examples = %v, want precious.toml only", paths)
	}
	loaded, err := Load(examplePath)
	if err != nil {
		t.Fatalf("example does not load: %v", err)
	}
	if loaded.StateDir != "/var/lib/precious" {
		t.Fatalf("example state_dir = %q, want /var/lib/precious", loaded.StateDir)
	}

	raw, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	setting := regexp.MustCompile(`^([a-z_]+) = `)
	defaulted := regexp.MustCompile(`^# ([a-z_]+ = .*)$`)
	table := regexp.MustCompile(`^\[([a-z_]+)\]$`)

	mentioned := map[string]bool{}
	var uncommented strings.Builder
	prefix := ""
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := sc.Text()
		if m := table.FindStringSubmatch(line); m != nil {
			prefix = m[1] + "."
		} else if m := defaulted.FindStringSubmatch(line); m != nil {
			line = m[1]
		}
		if m := setting.FindStringSubmatch(line); m != nil {
			mentioned[prefix+m[1]] = true
		}
		uncommented.WriteString(line + "\n")
	}

	var want []string
	for _, k := range configKeys(t) {
		want = append(want, k.name)
	}
	var got []string
	for k := range mentioned {
		got = append(got, k)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("example keys differ from Config\n got: %v\nwant: %v", got, want)
	}

	withDefaults, err := Load(writeConfig(t, uncommented.String()))
	if err != nil {
		t.Fatalf("example with defaults uncommented does not load: %v", err)
	}
	// "key = []" decodes to an empty list; the default is nil.
	for _, lists := range [][2]*[]string{
		{&withDefaults.Server.TrustedProxies, &loaded.Server.TrustedProxies},
		{&withDefaults.Sources.AllowedRoots, &loaded.Sources.AllowedRoots},
	} {
		if len(*lists[0]) == 0 && len(*lists[1]) == 0 {
			*lists[0], *lists[1] = nil, nil
		}
	}
	if !reflect.DeepEqual(withDefaults, loaded) {
		t.Errorf("a commented value in the example is not the default:\nuncommented: %+v\n    default: %+v", withDefaults, loaded)
	}
}

// TestOperatorDocsConfigReference checks the "Configuration reference" table in
// docs/operator.md against Config: the same set of keys, each documented type
// matching the Go type, and each documented default decoding to the real one.
func TestOperatorDocsConfigReference(t *testing.T) {
	raw, err := os.ReadFile(operatorDocs)
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## Configuration reference\n")
	if !ok {
		t.Fatal("docs/operator.md has no Configuration reference section")
	}
	section, _, _ = strings.Cut(section, "\n## ")

	row := regexp.MustCompile("^\\| `([a-z_.]+)` \\| ([^|]+) \\| ([^|]+) \\| [^|]+ \\|$")
	type docRow struct{ typ, def string }
	documented := map[string]docRow{}
	for _, line := range strings.Split(section, "\n") {
		if m := row.FindStringSubmatch(line); m != nil {
			if _, dup := documented[m[1]]; dup {
				t.Errorf("key %s documented twice", m[1])
			}
			documented[m[1]] = docRow{strings.TrimSpace(m[2]), strings.TrimSpace(m[3])}
		}
	}

	typeNames := map[reflect.Type]string{
		reflect.TypeFor[string]():   "string",
		reflect.TypeFor[bool]():     "boolean",
		reflect.TypeFor[int]():      "integer",
		reflect.TypeFor[int64]():    "integer",
		reflect.TypeFor[Duration](): "duration",
		reflect.TypeFor[[]string](): "array of strings",
	}
	for _, k := range configKeys(t) {
		d, ok := documented[k.name]
		if !ok {
			t.Errorf("Config key %s is not in the docs reference table", k.name)
			continue
		}
		delete(documented, k.name)
		if want, ok := typeNames[k.typ]; !ok || d.typ != want {
			t.Errorf("%s: documented type %q, Go type %v", k.name, d.typ, k.typ)
		}
		if d.def == "required" {
			if !k.def.IsZero() {
				t.Errorf("%s: documented as required but defaults to %v", k.name, k.def)
			}
			continue
		}
		lit, ok := strings.CutPrefix(d.def, "`")
		lit, ok2 := strings.CutSuffix(lit, "`")
		if !ok || !ok2 {
			t.Errorf("%s: default %q is not a `TOML literal`", k.name, d.def)
			continue
		}
		decoded := reflect.New(reflect.StructOf([]reflect.StructField{{Name: "V", Type: k.typ, Tag: `toml:"v"`}}))
		if _, err := toml.Decode("v = "+lit, decoded.Interface()); err != nil {
			t.Errorf("%s: default %s does not decode: %v", k.name, lit, err)
			continue
		}
		got := decoded.Elem().Field(0)
		emptyLists := got.Kind() == reflect.Slice && got.Len() == 0 && k.def.Len() == 0
		if !emptyLists && !reflect.DeepEqual(got.Interface(), k.def.Interface()) {
			t.Errorf("%s: documented default %s, real default %v", k.name, lit, k.def)
		}
	}
	for name := range documented {
		t.Errorf("docs reference table documents %s, which is not a Config key", name)
	}
}
