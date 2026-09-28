package selfupdate

import "testing"

func TestParseVersionAcceptsBothInjectionStyles(t *testing.T) {
	for input, want := range map[string]string{
		"v0.4.1":            "0.4.1",
		"0.4.1":             "0.4.1",
		"v1.2.3-beta.1":     "1.2.3-beta.1",
		"1.2.3+build.5":     "1.2.3+build.5",
		"v10.20.30-rc.1+x1": "10.20.30-rc.1+x1",
		" 1.2.3 ":           "1.2.3", // surrounding whitespace is trimmed
	} {
		v, err := ParseVersion(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if v.String() != want {
			t.Errorf("%q: got %q want %q", input, v.String(), want)
		}
	}
}

func TestParseVersionRejectsInvalid(t *testing.T) {
	for _, input := range []string{"", "dev", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "1.2.3-01", "v", "vv1.2.3", "1.2.3/../x", "1.2.3?x=1", "latest"} {
		if _, err := ParseVersion(input); err == nil {
			t.Errorf("%q unexpectedly parsed", input)
		}
	}
}

func TestCompareFollowsSemverPrecedence(t *testing.T) {
	ordered := []string{"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(mustVersion(t, ordered[i]), mustVersion(t, ordered[j])); got != want {
				t.Errorf("Compare(%s,%s)=%d want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if Compare(mustVersion(t, "1.0.0+a"), mustVersion(t, "1.0.0+b")) != 0 {
		t.Error("build metadata must not affect precedence")
	}
	if Compare(mustVersion(t, "0.10.0"), mustVersion(t, "0.9.0")) <= 0 {
		t.Error("numeric, not lexical, comparison expected")
	}
}

func TestValidTagIsStrict(t *testing.T) {
	for _, tag := range []string{"v1.2.3", "v0.4.10", "v1.0.0-rc.1"} {
		if !ValidTag(tag) {
			t.Errorf("%q should be valid", tag)
		}
	}
	for _, tag := range []string{"1.2.3", "v1.2", "v1.2.3/../../x", "v1.2.3?a=b", "v1.2.3#x", "latest", "", "v1.2.3\n", "V1.2.3", "v1.2.3 "} {
		if ValidTag(tag) {
			t.Errorf("%q should be invalid", tag)
		}
	}
}

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
