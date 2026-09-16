package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/deeprath/commerce-platform/pkg/config"
)

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}

func TestString(t *testing.T) {
	t.Setenv("SET", "value")
	if got := config.String("SET", "def"); got != "value" {
		t.Errorf("String(set) = %q", got)
	}
	if got := config.String("NEVER_SET_ANYWHERE", "def"); got != "def" {
		t.Errorf("String(unset) = %q", got)
	}
	// Documented: an empty value is treated as absent, so a variable blanked in
	// a compose file falls back rather than silently configuring "".
	t.Setenv("EMPTY", "")
	if got := config.String("EMPTY", "def"); got != "def" {
		t.Errorf("String(empty) = %q, want the default", got)
	}
}

func TestMustString(t *testing.T) {
	t.Setenv("SET", "value")
	if got := config.MustString("SET"); got != "value" {
		t.Errorf("MustString = %q", got)
	}
	mustPanic(t, "MustString(unset)", func() { config.MustString("NEVER_SET_ANYWHERE") })

	// Empty must be as fatal as unset: a secret that mounted as an empty file
	// is exactly the case this is meant to catch at startup.
	t.Setenv("EMPTY", "")
	mustPanic(t, `MustString("")`, func() { config.MustString("EMPTY") })
}

func TestInt(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want int
	}{
		{"8080", 8080},
		{"0", 0},
		{"-1", -1},
	} {
		t.Setenv("N", tc.val)
		if got := config.Int("N", 99); got != tc.want {
			t.Errorf("Int(%q) = %d, want %d", tc.val, got, tc.want)
		}
	}
	if got := config.Int("NEVER_SET_ANYWHERE", 99); got != 99 {
		t.Errorf("Int(unset) = %d", got)
	}
	// Fails loudly at startup rather than running on a default the operator
	// did not choose.
	for _, bad := range []string{"eight", "8.5", "8080x"} {
		t.Setenv("N", bad)
		mustPanic(t, "Int("+bad+")", func() { config.Int("N", 99) })
	}
}

func TestDuration(t *testing.T) {
	t.Setenv("D", "1500ms")
	if got := config.Duration("D", time.Second); got != 1500*time.Millisecond {
		t.Errorf("Duration = %v", got)
	}
	if got := config.Duration("NEVER_SET_ANYWHERE", time.Second); got != time.Second {
		t.Errorf("Duration(unset) = %v", got)
	}
	// A bare number is the classic mistake — "30" meaning 30 seconds. Go needs
	// a unit, and guessing one would be worse than refusing.
	for _, bad := range []string{"30", "1 second", "forever"} {
		t.Setenv("D", bad)
		mustPanic(t, "Duration("+bad+")", func() { config.Duration("D", time.Second) })
	}
}

func TestBool_AcceptsTheUsualSpellings(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "True", "t", "yes", "YES", "Yes", "on", "ON"} {
		t.Setenv("B", v)
		if !config.Bool("B", false) {
			t.Errorf("Bool(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"0", "false", "FALSE", "False", "f", "no", "NO", "off", "OFF"} {
		t.Setenv("B", v)
		if config.Bool("B", true) {
			t.Errorf("Bool(%q) = true, want false", v)
		}
	}
}

func TestBool_UnsetUsesTheDefault(t *testing.T) {
	if !config.Bool("NEVER_SET_ANYWHERE", true) {
		t.Error("Bool(unset, true) = false")
	}
	if config.Bool("NEVER_SET_ANYWHERE", false) {
		t.Error("Bool(unset, false) = true")
	}
}

// The important one. Both booleans in the platform are secure when true —
// COOKIE_SECURE and MINIO_USE_SSL — so a value Bool cannot read must never
// resolve to false. Falling through to false turns a typo in a values file
// into auth cookies served without the Secure flag, in production, with
// nothing logged and nothing failing. Int and Duration already panic on input
// they cannot parse; this makes Bool behave the same way.
func TestBool_UnreadableValuePanicsInsteadOfDisablingTheFlag(t *testing.T) {
	for _, bad := range []string{"ture", "enabled", "2", "-1", "null", "maybe", "tru"} {
		t.Setenv("COOKIE_SECURE", bad)
		mustPanic(t, "Bool("+bad+")", func() { config.Bool("COOKIE_SECURE", true) })
	}
}

// Secrets mounted from files routinely arrive with a trailing newline.
func TestBool_TolerateSurroundingWhitespace(t *testing.T) {
	for _, v := range []string{"true\n", " true", "true\r\n", "\ttrue\t"} {
		t.Setenv("B", v)
		if !config.Bool("B", false) {
			t.Errorf("Bool(%q) = false; a mounted secret's newline flipped the flag", v)
		}
	}
}

func TestLoadService_Defaults(t *testing.T) {
	for _, k := range []string{"ENVIRONMENT", "SERVICE_VERSION", "GRPC_ADDR", "OTEL_EXPORTER_OTLP_ENDPOINT", "LOG_LEVEL"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Setenv(k, "")
		}
	}
	svc := config.LoadService("catalog")

	// ENVIRONMENT defaulting to "local" is load-bearing beyond logging: the BFF
	// derives COOKIE_SECURE from it, so the default has to be the safe-to-run
	// one, and any deployment must set it explicitly.
	for _, tc := range []struct{ field, got, want string }{
		{"Name", svc.Name, "catalog"},
		{"Environment", svc.Environment, "local"},
		{"Version", svc.Version, "dev"},
		{"GRPCAddr", svc.GRPCAddr, ":50051"},
		{"OTLPEndpoint", svc.OTLPEndpoint, "otel-collector:4317"},
		{"LogLevel", svc.LogLevel, "info"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
}

func TestLoadService_EnvironmentOverrides(t *testing.T) {
	t.Setenv("ENVIRONMENT", "prod")
	t.Setenv("SERVICE_VERSION", "1.2.3")
	t.Setenv("GRPC_ADDR", ":9000")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector.obs:4317")
	t.Setenv("LOG_LEVEL", "debug")

	svc := config.LoadService("order")
	if svc.Name != "order" || svc.Environment != "prod" || svc.Version != "1.2.3" ||
		svc.GRPCAddr != ":9000" || svc.OTLPEndpoint != "collector.obs:4317" || svc.LogLevel != "debug" {
		t.Fatalf("LoadService = %+v", svc)
	}
}

// A value that is only whitespace is not a readable bool. It reaches Bool as a
// non-empty string, so it cannot fall back to the default the way an unset
// variable does — an empty secret would otherwise silently pick the default
// instead of reporting that it mounted empty.
func TestBool_WhitespaceOnlyIsNotADefault(t *testing.T) {
	for _, v := range []string{" ", "\n", "\t"} {
		t.Setenv("COOKIE_SECURE", v)
		mustPanic(t, "Bool(whitespace)", func() { config.Bool("COOKIE_SECURE", true) })
	}
}

// Every value the deployments actually set must keep parsing to what it parsed
// before, or this tightening breaks a running environment.
func TestBool_DeployedValuesStillParse(t *testing.T) {
	for _, tc := range []struct {
		val  string // as written in compose / the Helm values files
		want bool
	}{
		{"true", true},   // values.yaml (production)
		{"false", false}, // values-dev.yaml and docker-compose.yml
	} {
		t.Setenv("COOKIE_SECURE", tc.val)
		if got := config.Bool("COOKIE_SECURE", true); got != tc.want {
			t.Errorf("COOKIE_SECURE=%q now reads as %v, want %v", tc.val, got, tc.want)
		}
	}
}
