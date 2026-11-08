package allowlist

import "testing"

func TestAllows(t *testing.T) {
	l, err := Parse([]string{
		"airflow.internal:8080",
		"db.internal:*",
		"*.corp.local:443",
		"10.0.0.0/16:8123",
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		target string
		want   bool
	}{
		{"airflow.internal:8080", true},
		{"AIRFLOW.internal:8080", true},
		{"airflow.internal.:8080", true},
		{"airflow.internal:8081", false},
		{"other.internal:8080", false},
		{"db.internal:5432", true},
		{"db.internal:1", true},
		{"a.corp.local:443", true},
		{"a.b.corp.local:443", true},
		{"corp.local:443", false},
		{"evilcorp.local:443", false},
		{"a.corp.local:444", false},
		{"10.0.3.4:8123", true},
		{"10.1.3.4:8123", false},
		{"10.0.3.4:80", false},
		{"169.254.169.254:80", false},
		{"no-port", false},
		{"host:0", false},
		{"host:99999", false},
	}
	for _, c := range cases {
		if _, _, got := l.Allows(c.target); got != c.want {
			t.Errorf("Allows(%q) = %v, want %v", c.target, got, c.want)
		}
	}
}

func TestCIDRDoesNotMatchHostnames(t *testing.T) {
	l, _ := Parse([]string{"10.0.0.0/8:80"})
	if _, _, ok := l.Allows("internal.example:80"); ok {
		t.Fatal("CIDR rule must not match hostnames")
	}
}

func TestParseRejectsBadEntries(t *testing.T) {
	for _, e := range []string{"nohostport", "host:", ":80", "host:abc", "host:0", "a*b.local:80", "10.0.0.0/99:80", "host name:80"} {
		if _, err := Parse([]string{e}); err == nil {
			t.Errorf("Parse(%q) should fail", e)
		}
	}
}

func TestEmptyListAllowsNothing(t *testing.T) {
	l, _ := Parse(nil)
	if _, _, ok := l.Allows("a:1"); ok {
		t.Fatal("empty list must deny everything")
	}
}
