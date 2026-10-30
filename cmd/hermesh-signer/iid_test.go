package main

import "testing"

// Interfaces whose IIDs Windows' headers spell out.
func TestWinrtIID(t *testing.T) {
	for sig, want := range map[string]string{
		asyncOperation("b1"): "cdb5efb3-5788-509d-9be1-71ccb8a3362a", // IAsyncOperation<Boolean>
		"pinterface({913337e9-11a1-4345-a3a2-4e7f956e222d};string)": "98b9acc1-4b56-532e-ac73-03d5291cca90", // IVector<String>
	} {
		if got := winrtIID(sig).String(); got != want {
			t.Errorf("%s: %s, want %s", sig, got, want)
		}
	}
}
