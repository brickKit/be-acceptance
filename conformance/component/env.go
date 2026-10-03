package compconf

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SecretsRoot is where brickKit mounts secret files in a container (be-protocol P2.7):
// /run/brickkit/secrets/<versioned service name>/<KEY>.
const SecretsRoot = "/run/brickkit/secrets"

// suiteValues are the addresses and identities the suite gives the component.
type suiteValues struct {
	FakeHost     string // how the container reaches the suite's fakes
	AuthzPort    int
	IAMPort      int
	ObserverPort int
	PeerHTTP     map[string]int // dependency ID -> fake-peer HTTP port
	PeerGRPC     map[string]int
	DB           dbIdentity
	Issuer       string
	Tenant       string
	HasNATS      bool
	Extra        map[string]string // overrides, also for secret keys (the value goes into the file)
}

// dbIdentity is the run's random database identity (sdk-redesign §4.2, F27).
type dbIdentity struct {
	Database, Owner, OwnerPassword, Runtime, RuntimePassword, Schema string
}

// tuning keys: shortened timers an operator could set too (be-protocol P2, "Notes").
var tuning = map[string]string{
	"EVENTS_BACKOFF":          "200ms,500ms,1s",
	"EVENTS_MAX_DELIVER":      "3",
	"GRPC_MAX_CONNECTION_AGE": "10s",
}

// componentEnv builds the container environment: configSchema defaults, then the suite's
// values, then tuning keys; only declared keys plus the platform's reserved names. A secret
// key's variable holds the path of its file; its value is returned in secrets.
func componentEnv(c *Component, v suiteValues) (env []string, secrets map[string]string, err error) {
	props := c.Manifest.ConfigSchema.Properties
	vals := map[string]string{}
	for k, p := range props {
		if p.Default != nil {
			vals[k] = formatDefault(p.Default)
		}
	}
	for k, val := range suiteProvided(v) {
		if _, ok := props[k]; ok {
			vals[k] = val
		}
	}
	for k, val := range tuning {
		if _, ok := props[k]; ok {
			vals[k] = val
		}
	}
	for k, val := range v.Extra {
		vals[k] = val
	}
	secrets = map[string]string{}
	for k, p := range props {
		if !p.Secret {
			continue
		}
		if val, ok := vals[k]; ok {
			secrets[k] = val
			vals[k] = SecretsRoot + "/" + c.ServiceName() + "/" + k
		}
	}
	var missing []string
	for _, k := range c.Manifest.ConfigSchema.Required {
		if _, ok := vals[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, nil, fmt.Errorf("required keys the suite has no value for: %s", strings.Join(missing, ", "))
	}
	vals["COMPONENT_ID"], vals["COMPONENT_VERSION"] = c.ID(), c.Version()
	for _, d := range c.Dependencies() {
		if p, ok := v.PeerHTTP[d.ID]; ok {
			vals[depVar(d.ID)+"_ENDPOINT"] = fmt.Sprintf("http://%s:%d", v.FakeHost, p)
		}
		if p, ok := v.PeerGRPC[d.ID]; ok {
			vals[depVar(d.ID)+"_GRPC_ENDPOINT"] = fmt.Sprintf("http://%s:%d", v.FakeHost, p)
		}
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+vals[k])
	}
	return env, secrets, nil
}

func suiteProvided(v suiteValues) map[string]string {
	m := map[string]string{}
	url := func(port int) string { return fmt.Sprintf("http://%s:%d", v.FakeHost, port) }
	if v.DB.Database != "" {
		m["PG_HOST"], m["PG_PORT"], m["PG_DATABASE"] = "pg", "5432", v.DB.Database
		m["PG_USER"], m["PG_PASSWORD_FILE"] = v.DB.Runtime, v.DB.RuntimePassword
		m["PG_OWNER_USER"], m["PG_OWNER_PASSWORD_FILE"] = v.DB.Owner, v.DB.OwnerPassword
		m["PG_SCHEMA"] = v.DB.Schema
	}
	if v.AuthzPort != 0 {
		m["AUTHZ_URL"], m["AUTHZ_GRPC_URL"] = url(v.AuthzPort), url(v.AuthzPort)
	}
	if v.IAMPort != 0 {
		m["IAM_URL"] = url(v.IAMPort)
	}
	if v.Issuer != "" {
		m["IAM_ISSUER"], m["TENANT_ID"] = v.Issuer, v.Tenant
	}
	if v.ObserverPort != 0 {
		m["OTEL_BASE_URL"] = url(v.ObserverPort)
	}
	m["NATS_URL"] = "nats://nats:4222"
	return m
}

// depVar is the variable stem brickKit derives from a component ID (erp/inventory ->
// ERP_INVENTORY).
func depVar(id string) string {
	return strings.ToUpper(strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(id))
}

func formatDefault(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
