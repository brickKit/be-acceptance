package compconf

import "gopkg.in/yaml.v3"

// Fixtures is the part of conformance/fixtures.yaml the suite reads (be-protocol
// schemas/fixtures.schema.json). Fields of profiles this suite version does not run are kept
// as raw values.
type Fixtures struct {
	Users        map[string]User                 `yaml:"users"`
	Grants       map[string]Grant                `yaml:"grants"`
	Resources    map[string]map[string]FixtureOp `yaml:"resources"`
	Dependencies map[string]map[string]Canned    `yaml:"dependencies"`
	Events       FixtureEvents                   `yaml:"events"`
	Jobs         FixtureJobs                     `yaml:"jobs"`
	Lock         *struct {
		SQL   string `yaml:"sql"`
		While string `yaml:"while"`
	} `yaml:"lock"`
	PIILog *struct {
		Via    string   `yaml:"via"`
		Msg    string   `yaml:"msg"`
		Fields []string `yaml:"fields"`
	} `yaml:"pii_log"`
	Blob any        `yaml:"blob"`
	Slow *FixtureOp `yaml:"slow"`
}

// User is a persona the fake identity provider signs tokens for.
type User struct {
	Sub         string         `yaml:"sub"`
	Roles       []string       `yaml:"roles"`
	DeptPath    *string        `yaml:"dept_path"`
	LegalEntity string         `yaml:"legal_entity"`
	Locale      string         `yaml:"locale"`
	Act         map[string]any `yaml:"act"`
}

// Grant is a role grant put into the fake authorization provider's bundle.
type Grant struct {
	Keys         []string            `yaml:"keys" json:"-"`
	Levels       map[string]string   `yaml:"levels" json:"levels,omitempty"`
	DefaultLevel string              `yaml:"default_level" json:"default_level,omitempty"`
	Values       map[string][]string `yaml:"values" json:"values,omitempty"`
	Until        any                 `yaml:"until" json:"until,omitempty"`
}

// FixtureOp is a fixtures operation: REST (method + path) or gRPC.
type FixtureOp struct {
	Method     string            `yaml:"method"`
	Path       string            `yaml:"path"`
	GRPC       string            `yaml:"grpc"`
	As         string            `yaml:"as"`
	Key        string            `yaml:"key"`
	BodyFile   string            `yaml:"body_file"`
	Body       map[string]any    `yaml:"body"`
	Query      map[string]string `yaml:"query"`
	ID         string            `yaml:"id"`
	Idempotent bool              `yaml:"idempotent"`
	TwoPhase   bool              `yaml:"two_phase"`
	UserFacing bool              `yaml:"user_facing"`
	// Fingerprint is nil when absent, empty when declared empty.
	Fingerprint []string          `yaml:"fingerprint"`
	Triggers    *Triggers         `yaml:"triggers"`
	Filters     map[string]Filter `yaml:"filters"`
	Sort        *struct {
		Param  string   `yaml:"param"`
		Masked []string `yaml:"masked"`
	} `yaml:"sort"`
	Range *struct {
		After       string `yaml:"after"`
		Before      string `yaml:"before"`
		IncludeCold string `yaml:"include_cold"`
	} `yaml:"range"`
	Expect *struct {
		Status int               `yaml:"status"`
		Reason string            `yaml:"reason"`
		Field  map[string]string `yaml:"field"`
	} `yaml:"expect"`
}

// Canned is a fake peer's answer to one dependency call.
type Canned struct {
	ResponseFile string         `yaml:"response_file"`
	Response     map[string]any `yaml:"response"`
	Code         string         `yaml:"code"`
	HTTP         string         `yaml:"http"`
	Hang         bool           `yaml:"hang"`
}

// Triggers says what an operation causes elsewhere.
type Triggers struct {
	GRPC  string `yaml:"grpc"`
	HTTP  string `yaml:"http"`
	Event string `yaml:"event"`
	Job   string `yaml:"job"`
}

// Filter maps a list parameter to the data-scope dimension its values belong to.
type Filter struct {
	Dimension string `yaml:"dimension"`
}

// Observe is how the suite sees a result: SQL on the component's own tables ($1 = the
// aggregate ID) or a request.
type Observe struct {
	SQL     string     `yaml:"sql"`
	Request *FixtureOp `yaml:"request"`
}

// FixtureEvents is fixtures events.
type FixtureEvents struct {
	Produces []Produce `yaml:"produces"`
	Consumes []Consume `yaml:"consumes"`
}

// UnmarshalYAML accepts produces as one entry or a list.
func (e *FixtureEvents) UnmarshalYAML(n *yaml.Node) error {
	var raw struct {
		Produces yaml.Node `yaml:"produces"`
		Consumes []Consume `yaml:"consumes"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	e.Consumes = raw.Consumes
	switch raw.Produces.Kind {
	case yaml.MappingNode:
		var p Produce
		if err := raw.Produces.Decode(&p); err != nil {
			return err
		}
		e.Produces = []Produce{p}
	case yaml.SequenceNode:
		return raw.Produces.Decode(&e.Produces)
	}
	return nil
}

// Produce says which operation makes the component publish a subject.
type Produce struct {
	Via     string `yaml:"via"`
	Subject string `yaml:"subject"`
}

// Consume is one subscribed subject.
type Consume struct {
	Subject     string  `yaml:"subject"`
	Consumer    string  `yaml:"consumer"`
	SampleFile  string  `yaml:"sample_file"`
	AggregateID string  `yaml:"aggregate_id"`
	Observe     Observe `yaml:"observe"`
	Setup       string  `yaml:"setup"`
	Produces    string  `yaml:"produces"`
}

// FixtureJobs is fixtures jobs.
type FixtureJobs struct {
	Enqueues []struct {
		Via     string  `yaml:"via"`
		Kind    string  `yaml:"kind"`
		Observe Observe `yaml:"observe"`
	} `yaml:"enqueues"`
	Cron []struct {
		Name     string         `yaml:"name"`
		Override map[string]any `yaml:"override"`
		Observe  Observe        `yaml:"observe"`
	} `yaml:"cron"`
	Reconcilers []struct {
		Name    string `yaml:"name"`
		Via     string `yaml:"via"`
		Stall   string `yaml:"stall"`
		Resolve struct {
			Call         string `yaml:"call"`
			ResponseFile string `yaml:"response_file"`
		} `yaml:"resolve"`
		Observe Observe `yaml:"observe"`
	} `yaml:"reconcilers"`
}

func yamlUnmarshal(b []byte, v any) error { return yaml.Unmarshal(b, v) }
