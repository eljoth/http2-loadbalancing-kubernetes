package main

import "testing"

func TestHeron(t *testing.T) {
	tests := []struct {
		name  string
		in    float64
		delta float64
		out   float64
	}{
		{"4", 4, 0.1, 2},
		{"9", 9, 0.0001, 3},
		{"2", 2, 0.0000000000001, 1.4142156862745097},
		{"0.25", 0.25, 0.0001, 0.5},
		{"0.5", 0.5, 0.0001, 0.7071067811865476},
		{"0.75", 0.75, 0.0000000000000001, 0.8660254037844386},
		{"0.125", 0.125, 0.0000000000000001, 0.35355339059327373},
		{"0.625", 0.625, 0.0001, 0.7905694150420948},
	}

	assertDelta := func(t *testing.T, got, want, delta float64) {
		t.Helper()
		if got-want > delta {
			t.Errorf("want %f, got %f", want, got)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			get := heron(tt.in, tt.delta)
			assertDelta(t, get, tt.out, tt.delta)
		})
	}
}
