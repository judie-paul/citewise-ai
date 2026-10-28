package models

import "testing"

func TestParseDOI(t *testing.T) {
	valid := map[string]string{
		"10.1080/09638237.2020.1714007":                            "10.1080/09638237.2020.1714007",
		"  https://doi.org/10.1080/09638237.2020.1714007":          "10.1080/09638237.2020.1714007",
		"http://dx.doi.org/10.1000/ABC":                            "10.1000/abc",
		"doi.org/10.1000/abc":                                      "10.1000/abc",
		"DOI: 10.1000/abc.":                                        "10.1000/abc",
		"doi:10.1000/abc":                                          "10.1000/abc",
		"https://doi.org/10.1080%2F09638237.2020.1714007":          "10.1080/09638237.2020.1714007",
		"10.1002/(sici)1097-0282(1996)40:1<1::aid-bip1>3.0.co;2-#": "10.1002/(sici)1097-0282(1996)40:1<1::aid-bip1>3.0.co;2-#",
	}
	for in, want := range valid {
		if got, ok := ParseDOI(in); !ok || got != want {
			t.Errorf("ParseDOI(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "hello", "10.1000", "10.1/abc", "11.1000/abc", "https://example.com/10.1000/abc", "10.1000/has space", "isbn 978-0-14-312779-6"} {
		if got, ok := ParseDOI(in); ok {
			t.Errorf("ParseDOI(%q) = %q, should be rejected", in, got)
		}
	}
}

func TestNormalizeDOI(t *testing.T) {
	if got := NormalizeDOI("https://doi.org/10.1/ABC"); got != "10.1/abc" {
		t.Fatalf("NormalizeDOI = %q", got)
	}
	if got := NormalizeDOI("not a doi"); got != "" {
		t.Fatalf("NormalizeDOI = %q", got)
	}
}
