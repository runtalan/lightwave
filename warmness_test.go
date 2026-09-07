package main

import "testing"

func TestWarmRGBKeepsCoolWhiteBrightForBLE(t *testing.T) {
	c := warmRGB(6000)
	if c.R != 255 || c.G < 240 || c.B < 230 {
		t.Fatalf("6000K = %#v; want a near-full-output cool white", c)
	}
	if c.Kelvin != 6000 {
		t.Fatalf("Kelvin = %d, want 6000", c.Kelvin)
	}
}

func TestWarmRGBWarmsAsKelvinFalls(t *testing.T) {
	warm, cool := warmRGB(2000), warmRGB(6000)
	if warm.B >= cool.B || warm.G >= cool.G {
		t.Fatalf("2000K %#v should be warmer than 6000K %#v", warm, cool)
	}
}
