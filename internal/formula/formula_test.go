package formula

import (
	"math"
	"strings"
	"testing"
)

func TestEval(t *testing.T) {
	vars := map[string]float64{"kills": 10, "deaths": 4, "teamkills": 1, "attendance": 3, "maxattendance": 9}
	cases := map[string]float64{
		"kills / deaths":                       2.5,
		"Kills / MAX(deaths, 1)":               2.5,
		"1 + 2 * 3":                            7,
		"(1 + 2) * 3":                          9,
		"2 ^ 3 ^ 2":                            512,
		"-2 ^ 2":                               -4,
		"10 % 4":                               2,
		"if(deaths > 5, 1, 2)":                 2,
		"if(deaths >= 4 && kills == 10, 7, 0)": 7,
		"!0 + !5":                              1,
		"round(10 / 3, 2)":                     3.33,
		"round(2.5)":                           3,
		"min(3, 1, 2) + max(1, 5)":             6,
		"log(100) + log(8, 2) + log10(1000)":   8,
		"ln(exp(2))":                           2,
		"sqrt(16) + abs(-1) + floor(1.7) + ceil(1.2)": 8,
		"pow(2, 10)":     1024,
		"1.5e2":          150,
		"unknownvar + 1": 1,
		"(kills - 2*teamKills) / (deaths + 1) * (0.7 + 0.3 * ln(attendance + 1) / ln(maxAttendance + 1))": 1.6 * (0.7 + 0.3*math.Log(4)/math.Log(10)),
		"round(kills; 0)": 10,
	}
	for src, want := range cases {
		e, err := Compile(src, nil)
		if err != nil {
			t.Errorf("Compile(%q): %v", src, err)
			continue
		}
		if got := e.Eval(vars); math.Abs(got-want) > 1e-9 {
			t.Errorf("%q = %v, want %v", src, got, want)
		}
	}
	e, _ := Compile("kills / 0", nil)
	if !math.IsInf(e.Eval(vars), 1) {
		t.Error("division by zero should give +Inf")
	}
}

func TestCompileErrors(t *testing.T) {
	known := []string{"kills", "deaths"}
	for src, want := range map[string]string{
		"":            "empty",
		"kills +":     "ends unexpectedly",
		"(kills":      "missing",
		"kills )":     "unexpected",
		"foo(1)":      "unknown function",
		"assists * 2": "unknown variable",
		"max()":       "wrong number",
		"if(1, 2)":    "3 arguments",
		"kills # 2":   "unexpected character",
		"1..2":        "invalid number",
		strings.Repeat("(", 100) + "1" + strings.Repeat(")", 100): "nested",
		strings.Repeat("1+", 600) + "1":                           "longer than",
	} {
		_, err := Compile(src, known)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Compile(%q) error = %v, want %q", src, err, want)
		}
	}
	e, err := Compile("Kills/max(DEATHS,1)", known)
	if err != nil || len(e.Vars()) != 2 {
		t.Errorf("vars: %v %v", e, err)
	}
}
