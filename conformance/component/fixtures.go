package compconf

// Fixtures is the part of conformance/fixtures.yaml the suite reads (be-protocol
// schemas/fixtures.schema.json). Fields of profiles this suite version does not run are kept
// as raw values.
type Fixtures struct {
	Users        map[string]User                 `yaml:"users"`
	Grants       map[string]Grant                `yaml:"grants"`
	Resources    map[string]map[string]FixtureOp `yaml:"resources"`
	Dependencies map[string]map[string]Canned    `yaml:"dependencies"`
	Events       any                             `yaml:"events"`
	Jobs         any                             `yaml:"jobs"`
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
	Expect     *struct {
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
	Hang         bool           `yaml:"hang"`
}
