package secrets

import (
	"strings"
	"testing"
)

func TestScan_AWS(t *testing.T) {
	src := []byte("config.access_key = \"AKIAIOSFODNN7EXAMPLE\"\n")
	f := Scan(src)
	if len(f) != 1 || f[0].Kind != "aws-access-key" || f[0].Line != 1 {
		t.Errorf("got %+v, want one aws-access-key on line 1", f)
	}
}

func TestScan_PrivateKeyBlock(t *testing.T) {
	src := []byte("// docs\nfoo\n-----BEGIN RSA PRIVATE KEY-----\nbase64...\n")
	f := Scan(src)
	if len(f) != 1 || f[0].Kind != "pem-private-key" || f[0].Line != 3 {
		t.Errorf("got %+v, want pem-private-key on line 3", f)
	}
}

func TestScan_MultiplePatterns(t *testing.T) {
	src := []byte("a := \"AKIAIOSFODNN7EXAMPLE\"\nb := \"AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY\"\n")
	f := Scan(src)
	if len(f) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(f), f)
	}
	got := map[string]int{}
	for _, x := range f {
		got[x.Kind] = x.Line
	}
	if got["aws-access-key"] != 1 || got["google-api-key"] != 2 {
		t.Errorf("findings = %v", got)
	}
}

func TestScan_NoFalsePositiveOnNormalCode(t *testing.T) {
	src := []byte("func main() {\n  x := \"hello world\"\n  fmt.Println(x)\n}\n")
	if f := Scan(src); len(f) != 0 {
		t.Errorf("expected no findings on plain Go; got %+v", f)
	}
}

func TestScan_DedupsSameLineSameKind(t *testing.T) {
	src := []byte("AKIAIOSFODNN7EXAMPLE AKIAIOSFODNN7EXAMPLE\n")
	f := Scan(src)
	if len(f) != 1 {
		t.Errorf("expected dedup; got %+v", f)
	}
}

func TestSummary(t *testing.T) {
	f := []Finding{
		{Kind: "aws-access-key", Line: 1},
		{Kind: "pem-private-key", Line: 5},
	}
	got := Summary(f)
	if !strings.Contains(got, "aws-access-key") || !strings.Contains(got, "pem-private-key") {
		t.Errorf("summary = %q", got)
	}
	if !strings.HasPrefix(got, "2 possible secrets") {
		t.Errorf("summary should start with count; got %q", got)
	}
	if got := Summary(nil); got != "" {
		t.Errorf("empty findings should yield empty summary; got %q", got)
	}
	one := []Finding{{Kind: "aws-access-key", Line: 1}}
	if got := Summary(one); !strings.HasPrefix(got, "1 possible secret ") {
		t.Errorf("singular: got %q", got)
	}
}

func TestScan_EmptyInput(t *testing.T) {
	if f := Scan(nil); f != nil {
		t.Errorf("nil input should return nil; got %+v", f)
	}
}
